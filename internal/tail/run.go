// Command entry for `raven tail`: the long-lived transcript tailer that speaks
// completed assistant text blocks during a turn — before the Stop hook fires —
// so multi-step turns aren't silent. It reads the selected session's transcript,
// tracks a durable per-session cursor + bounded seen-set, and respects channel
// selection.
//
// Gated by LIVE_NARRATION (config.sh): when off, it only shadow-logs and never
// touches the queue. When on, it enqueues each completed block through the same
// caption+.txt commit protocol the hook uses, and the Stop hook yields to it (so
// the final block isn't spoken twice). If the tailer is down, the Stop hook
// falls back to speaking the final block itself.
package tail

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"raven-go/internal/clean"
	"raven-go/internal/config"
	"raven-go/internal/hook"
	"raven-go/internal/rlog"
	"raven-go/internal/state"
)

// caption mirrors the hook's queue caption so the phone transcript renders live-
// narrated blocks identically to Stop-hook replies.
type caption struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	Project   string `json:"project"`
	Text      string `json:"text"`
	Display   string `json:"display"`
}

// cursor is the durable per-session tail state (tail-state/<session>.json). The
// byte offset handles normal append progress; the seen-set handles restarts and
// reprocessing around a line boundary. device/inode detect file rotation.
type cursor struct {
	Version   int      `json:"version"`
	SessionID string   `json:"session_id"`
	Path      string   `json:"transcript_path"`
	Device    uint64   `json:"device"`
	Inode     uint64   `json:"inode"`
	Offset    int64    `json:"offset"`
	Seen      []string `json:"seen"`
}

const seenCap = 2048 // newest N block keys retained across restarts

type runner struct {
	home   string
	replay bool // start a fresh session at offset 0 (inspection) instead of EOF
}

// Run is the `raven tail` entry point.
func Run(args []string) error {
	fs := flag.NewFlagSet("tail", flag.ContinueOnError)
	interval := fs.Int("interval-ms", 300, "poll interval in milliseconds")
	once := fs.Bool("once", false, "run a single pass and exit (tests/inspection)")
	replay := fs.Bool("replay", false, "baseline a new session at offset 0 and read history (inspection; never used live)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	home := hook.Home()
	if fi, err := os.Stat(home); err != nil || !fi.IsDir() {
		return fmt.Errorf("home not found: %s", home)
	}
	_ = os.MkdirAll(filepath.Join(home, "tail-state"), 0o755)
	r := &runner{home: home, replay: *replay}

	if !*once {
		pidPath := filepath.Join(home, ".tail.pid")
		_ = os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0o644)
		defer os.Remove(pidPath)
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
		go func() { <-sig; os.Remove(pidPath); os.Exit(0) }()
	}

	mode := "shadow"
	if config.Load(home).LiveNarration {
		mode = "live"
	}
	_ = os.MkdirAll(filepath.Join(home, "queue"), 0o755)
	rlog.Log(home, "tail", "start", map[string]any{
		"mode": mode, "interval_ms": *interval, "replay": *replay,
	})
	for {
		r.pass()
		if *once {
			return nil
		}
		time.Sleep(time.Duration(*interval) * time.Millisecond)
	}
}

// pass runs one poll: resolve the selected session, advance its cursor over any
// newly-appended complete lines, and (shadow) log the eligible blocks.
func (r *runner) pass() {
	session, path, project := selectedTarget(r.home)
	if session == "" || path == "" {
		return
	}
	fi, err := os.Stat(path)
	if err != nil {
		return
	}
	dev, ino := devIno(fi)
	size := fi.Size()
	cfg := config.Load(r.home)

	cur := loadCursor(r.home, session)
	// New session, changed path/inode, or a shrunk file (rotation): re-baseline.
	// Default baseline is EOF — never narrate historical transcript while driving.
	if cur == nil || cur.Path != path || cur.Device != dev || cur.Inode != ino || size < cur.Offset {
		base := size
		if r.replay && (cur == nil || cur.Path != path) {
			base = 0 // inspection: read the whole file from the start
		}
		if cur != nil && cur.Path == path && size < cur.Offset {
			rlog.Log(r.home, "tail", "rotation_reset", map[string]any{"session": session})
			base = size // a shrunk/replaced file always resets to EOF, even under replay
		}
		cur = &cursor{Version: 1, SessionID: session, Path: path, Device: dev, Inode: ino, Offset: base}
	}
	if size == cur.Offset {
		saveCursor(r.home, session, cur) // persist the baseline; nothing new to read
		return
	}

	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	if _, err := f.Seek(cur.Offset, io.SeekStart); err != nil {
		return
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return
	}

	seen := sliceToSet(cur.Seen)
	blocks, consumed := ParseNewBlocks(data, cur.Offset, session, seen, cfg.MaxSpokenChars)
	if consumed == 0 {
		return // only an unterminated line so far; wait for it to complete
	}
	for _, b := range blocks {
		if cfg.LiveNarration {
			if enqueueBlock(r.home, session, project, b) {
				rlog.Log(r.home, "tail", "narrated", map[string]any{
					"session": session, "uuid": b.UUID, "index": b.Index,
					"chars": len(b.Cleaned), "preview": preview(b.Cleaned, 80),
				})
			} else {
				// Selection changed at commit time (or a rare write failure): don't
				// speak into the wrong channel. Mark seen anyway — we never backfill
				// a block on re-selection.
				rlog.Log(r.home, "tail", "narrate_skip", map[string]any{"session": session, "uuid": b.UUID})
			}
		} else {
			rlog.Log(r.home, "tail", "shadow_block", map[string]any{
				"session": session, "uuid": b.UUID, "index": b.Index,
				"key": short(b.Key), "text_hash": short(b.TextHash),
				"chars": len(b.Cleaned), "preview": preview(b.Cleaned, 80), "ts": b.Timestamp,
			})
		}
		cur.Seen = append(cur.Seen, b.Key)
	}
	cur.Offset += consumed
	if len(cur.Seen) > seenCap {
		cur.Seen = cur.Seen[len(cur.Seen)-seenCap:]
	}
	saveCursor(r.home, session, cur)
}

// selectedTarget returns the currently-selected session, its transcript path,
// and project, read together under the state lock so selection and channel
// metadata agree.
func selectedTarget(home string) (session, path, project string) {
	if unlock, err := state.Lock(home); err == nil {
		defer unlock()
	}
	session = state.SelectedSession(home)
	if session == "" {
		return "", "", ""
	}
	for _, c := range state.ReadChannels(home) {
		if c.SessionID == session {
			return session, c.TranscriptPath, c.Project
		}
	}
	return session, "", "" // selected but no known transcript path yet
}

// enqueueBlock commits one block to the speech queue exactly like the hook does:
// metadata first, then the .txt rename as the commit marker. It re-checks the
// selection under the state lock immediately before committing so a block is
// never spoken into a channel that was deselected mid-poll. Returns false if the
// session is no longer selected or the write fails.
func enqueueBlock(home, session, project string, b Block) bool {
	unlock, err := state.Lock(home)
	if err == nil {
		defer unlock()
	}
	if state.SelectedSession(home) != session {
		return false
	}
	stamp := strconv.FormatInt(time.Now().UnixNano(), 10)
	// No "In <project>." prefix: continuous mid-turn narration would repeat it on
	// every block, which is grating on a drive. Project still rides in the caption
	// for the phone transcript. (Tunable after the first drive.)
	spoken := b.Cleaned
	meta, err := json.Marshal(caption{
		ID: stamp, SessionID: session, Project: project,
		Text: b.Cleaned, Display: clean.Display(b.Raw),
	})
	if err != nil {
		return false
	}
	q := filepath.Join(home, "queue")
	if !writeAtomic(filepath.Join(q, stamp+".caption.json"), meta) {
		return false
	}
	if !writeAtomic(filepath.Join(q, stamp+".txt"), []byte(spoken)) {
		os.Remove(filepath.Join(q, stamp+".caption.json"))
		return false
	}
	return true
}

func devIno(fi os.FileInfo) (dev, ino uint64) {
	if st, ok := fi.Sys().(*syscall.Stat_t); ok {
		return uint64(st.Dev), uint64(st.Ino)
	}
	return 0, 0
}

func cursorPath(home, session string) string {
	return filepath.Join(home, "tail-state", session+".json")
}

func loadCursor(home, session string) *cursor {
	b, err := os.ReadFile(cursorPath(home, session))
	if err != nil {
		return nil
	}
	var c cursor
	if json.Unmarshal(b, &c) != nil {
		return nil
	}
	return &c
}

func saveCursor(home, session string, c *cursor) {
	b, err := json.Marshal(c)
	if err != nil {
		return
	}
	writeAtomic(cursorPath(home, session), b)
}

func writeAtomic(path string, data []byte) bool {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp.*")
	if err != nil {
		return false
	}
	name := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(name)
		return false
	}
	tmp.Sync()
	tmp.Close()
	if os.Rename(name, path) != nil {
		os.Remove(name)
		return false
	}
	return true
}

func sliceToSet(keys []string) map[string]struct{} {
	m := make(map[string]struct{}, len(keys))
	for _, k := range keys {
		m[k] = struct{}{}
	}
	return m
}

func short(s string) string {
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func preview(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}
