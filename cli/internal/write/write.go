// Package write produces Raven's uninterrupted 24 kHz mono s16le timeline.
// ffmpeg remains responsible for PCM generation and decoding; this package
// preserves writer.sh's queue selection, listener gate, and emit sequencing.
package write

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"raven-go/internal/config"
	"raven-go/internal/hook"
	"raven-go/internal/rlog"
	"raven-go/internal/transcript"
)

const (
	listenerMaxAge = 10 * time.Second
	textMinAge     = 5 * time.Second
	queueMaxAge    = 10 * time.Minute
)

type writer struct {
	home       string
	queue      string
	heartbeat  string
	synthdPID  string
	idleFloor  string
	stdout     io.Writer
	now        func() time.Time
	processUp  func(string) bool
	runCommand func(string, []string, io.Writer) bool
}

// Run validates the write command and then emits PCM forever, exactly as
// writer.sh does. stdout is normally os.Stdout, wired to pcm.fifo by start.sh.
func Run(args []string, stdout io.Writer) error {
	if len(args) != 0 {
		return fmt.Errorf("write: unexpected arguments: %s", strings.Join(args, " "))
	}
	if stdout == nil {
		return errors.New("write: stdout is unavailable")
	}
	home := hook.Home()
	info, err := os.Stat(home)
	if err != nil {
		return fmt.Errorf("write: Raven home: %w", err)
	}
	if !info.IsDir() {
		return fmt.Errorf("write: Raven home is not a directory: %s", home)
	}

	cfg := config.Load(home)
	w := writer{
		home:       home,
		queue:      filepath.Join(home, "queue"),
		heartbeat:  filepath.Join(home, "hls", ".heartbeat"),
		synthdPID:  filepath.Join(home, ".synthd.pid"),
		idleFloor:  cfg.IdleFloor,
		stdout:     stdout,
		now:        time.Now,
		processUp:  synthdAlive,
		runCommand: runCommand,
	}
	for {
		w.step()
	}
}

func (w *writer) step() {
	now := w.now()
	live := listenerLive(w.heartbeat, now)
	cleanupStale(w.queue, now)
	clip := pickNextClip(w.queue, now, func() bool { return w.processUp(w.synthdPID) })
	if live && clip != "" {
		w.emitClip(clip)
		return
	}
	w.emitIdle()
}

func (w *writer) emitIdle() {
	source := "anoisesrc=r=24000:c=pink:a=0.002:d=0.25"
	if w.idleFloor == "silence" {
		source = "anullsrc=r=24000:cl=mono:d=0.25"
	}
	w.runFFmpeg([]string{
		"-loglevel", "quiet", "-f", "lavfi", "-i", source,
		"-f", "s16le", "-ar", "24000", "-ac", "1", "-acodec", "pcm_s16le", "-",
	})
}

func (w *writer) emitClip(clip string) {
	stem := strings.TrimSuffix(clip, filepath.Ext(clip))
	caption := stem + ".caption.json"

	// This pink pre-roll is deliberately emitted before transcript/log updates,
	// matching writer.sh and waking car amplifiers before the first word.
	w.runFFmpeg([]string{
		"-loglevel", "quiet", "-f", "lavfi", "-i", "anoisesrc=r=24000:c=pink:a=0.002:d=0.35",
		"-f", "s16le", "-ar", "24000", "-ac", "1", "-acodec", "pcm_s16le", "-",
	})

	transcript.AddClaude(w.home, caption)
	rlog.Log(w.home, "writer", "emit", map[string]any{"id": filepath.Base(stem)})

	if filepath.Ext(clip) == ".txt" {
		w.emitSayFallback(clip)
	} else {
		w.decode(clip)
	}
	_ = os.Remove(clip)
	_ = os.Remove(caption)
}

func (w *writer) emitSayFallback(textPath string) {
	b, err := os.ReadFile(textPath)
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp("", "spk")
	if err != nil {
		return
	}
	base := tmp.Name()
	_ = tmp.Close()
	aiff := base + ".aiff"
	defer os.Remove(base)
	defer os.Remove(aiff)

	if w.runCommand("say", []string{"-o", aiff, string(b)}, io.Discard) {
		w.decode(aiff)
	}
}

func (w *writer) decode(path string) {
	w.runFFmpeg([]string{
		"-nostdin", "-loglevel", "quiet", "-i", path,
		"-f", "s16le", "-ar", "24000", "-ac", "1", "-acodec", "pcm_s16le", "-",
	})
}

func (w *writer) runFFmpeg(args []string) bool {
	return w.runCommand("ffmpeg", args, w.stdout)
}

func runCommand(name string, args []string, stdout io.Writer) bool {
	cmd := exec.Command(name, args...)
	cmd.Stdout = stdout
	cmd.Stderr = io.Discard
	return cmd.Run() == nil
}

func listenerLive(heartbeat string, now time.Time) bool {
	info, err := os.Stat(heartbeat)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	return now.Sub(info.ModTime()) <= listenerMaxAge
}

func cleanupStale(queue string, now time.Time) {
	entries, err := os.ReadDir(queue)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !staleQueueName(entry.Name()) {
			continue
		}
		info, err := entry.Info()
		if err == nil && now.Sub(info.ModTime()) > queueMaxAge {
			_ = os.Remove(filepath.Join(queue, entry.Name()))
		}
	}
}

func staleQueueName(name string) bool {
	return strings.HasSuffix(name, ".txt") ||
		strings.HasSuffix(name, ".aiff") ||
		strings.HasSuffix(name, ".wav") ||
		strings.HasSuffix(name, ".caption.json")
}

// pickNextClip preserves the queue's format priority: the oldest named ready
// WAV, then AIFF, then an old-enough text fallback only while synthd is down.
func pickNextClip(queue string, now time.Time, synthdUp func() bool) string {
	entries, err := os.ReadDir(queue)
	if err != nil {
		return ""
	}
	// os.ReadDir is sorted, but keep this explicit: timestamp filenames make
	// lexical order the queue's oldest-first order.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	for _, suffix := range []string{".wav", ".aiff"} {
		for _, entry := range entries {
			if !entry.IsDir() && strings.HasSuffix(entry.Name(), suffix) {
				return filepath.Join(queue, entry.Name())
			}
		}
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".txt") {
			continue
		}
		if synthdUp() {
			return ""
		}
		path := filepath.Join(queue, entry.Name())
		info, err := os.Stat(path)
		if err == nil && now.Sub(info.ModTime()) >= textMinAge {
			return path
		}
		// writer.sh examines only the oldest text file. A fresh oldest item
		// holds later text items even if their mtimes are unusual.
		return ""
	}
	return ""
}

func synthdAlive(pidPath string) bool {
	b, err := os.ReadFile(pidPath)
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return false
	}
	return syscall.Kill(pid, 0) == nil
}
