package write

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestPickNextClipPriorityAndOldest(t *testing.T) {
	queue := t.TempDir()
	now := time.Unix(1_800_000_000, 0)
	for _, name := range []string{"200.wav", "100.wav", "050.aiff", "010.txt"} {
		writeAt(t, filepath.Join(queue, name), now.Add(-20*time.Second))
	}

	if got, want := filepath.Base(pickNextClip(queue, now, down)), "100.wav"; got != want {
		t.Fatalf("ready WAV must win and be oldest by name: got %q want %q", got, want)
	}
	removeSuffix(t, queue, ".wav")
	if got, want := filepath.Base(pickNextClip(queue, now, down)), "050.aiff"; got != want {
		t.Fatalf("AIFF must follow WAV: got %q want %q", got, want)
	}
	removeSuffix(t, queue, ".aiff")
	if got, want := filepath.Base(pickNextClip(queue, now, down)), "010.txt"; got != want {
		t.Fatalf("stale text must be the last-resort choice: got %q want %q", got, want)
	}
	if got := pickNextClip(queue, now, up); got != "" {
		t.Fatalf("live synthd must hold text indefinitely, got %q", got)
	}
}

func TestPickNextClipTextMustBeFiveSecondsOld(t *testing.T) {
	queue := t.TempDir()
	now := time.Unix(1_800_000_000, 0)
	path := filepath.Join(queue, "100.txt")

	writeAt(t, path, now.Add(-5*time.Second+time.Nanosecond))
	if got := pickNextClip(queue, now, down); got != "" {
		t.Fatalf("text younger than five seconds must wait, got %q", got)
	}
	if err := os.Chtimes(path, now.Add(-5*time.Second), now.Add(-5*time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := pickNextClip(queue, now, down); got != path {
		t.Fatalf("five-second-old text must be eligible: got %q want %q", got, path)
	}
}

func TestPickNextClipFreshOldestTextHoldsLaterText(t *testing.T) {
	queue := t.TempDir()
	now := time.Unix(1_800_000_000, 0)
	writeAt(t, filepath.Join(queue, "100.txt"), now.Add(-time.Second))
	writeAt(t, filepath.Join(queue, "200.txt"), now.Add(-20*time.Second))
	if got := pickNextClip(queue, now, down); got != "" {
		t.Fatalf("only the oldest named text is examined, got %q", got)
	}
}

func TestCleanupStaleQueueFiles(t *testing.T) {
	queue := t.TempDir()
	now := time.Unix(1_800_000_000, 0)
	stale := []string{"old.txt", "old.aiff", "old.wav", "old.caption.json"}
	for _, name := range stale {
		writeAt(t, filepath.Join(queue, name), now.Add(-10*time.Minute-time.Nanosecond))
	}
	kept := []string{"edge.wav", "fresh.caption.json", "unrelated.json"}
	writeAt(t, filepath.Join(queue, kept[0]), now.Add(-10*time.Minute))
	writeAt(t, filepath.Join(queue, kept[1]), now.Add(-time.Minute))
	writeAt(t, filepath.Join(queue, kept[2]), now.Add(-time.Hour))

	cleanupStale(queue, now)
	for _, name := range stale {
		if _, err := os.Stat(filepath.Join(queue, name)); !os.IsNotExist(err) {
			t.Errorf("stale queue file %s was not deleted", name)
		}
	}
	for _, name := range kept {
		if _, err := os.Stat(filepath.Join(queue, name)); err != nil {
			t.Errorf("file %s should have been kept: %v", name, err)
		}
	}
}

func TestListenerLiveGate(t *testing.T) {
	dir := t.TempDir()
	heartbeat := filepath.Join(dir, ".heartbeat")
	now := time.Unix(1_800_000_000, 0)
	if listenerLive(heartbeat, now) {
		t.Fatal("missing heartbeat must not be live")
	}
	writeAt(t, heartbeat, now.Add(-10*time.Second))
	if !listenerLive(heartbeat, now) {
		t.Fatal("heartbeat exactly ten seconds old must be live")
	}
	if err := os.Chtimes(heartbeat, now.Add(-10*time.Second-time.Nanosecond), now.Add(-10*time.Second-time.Nanosecond)); err != nil {
		t.Fatal(err)
	}
	if listenerLive(heartbeat, now) {
		t.Fatal("heartbeat older than ten seconds must not be live")
	}
	if err := os.Chtimes(heartbeat, now.Add(time.Second), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if !listenerLive(heartbeat, now) {
		t.Fatal("future heartbeat must be treated as live")
	}
}

func TestSynthdAlive(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, ".synthd.pid")
	if synthdAlive(pidPath) {
		t.Fatal("missing PID file must be down")
	}
	if err := os.WriteFile(pidPath, []byte("not-a-pid"), 0o644); err != nil {
		t.Fatal(err)
	}
	if synthdAlive(pidPath) {
		t.Fatal("invalid PID must be down")
	}
	if err := os.WriteFile(pidPath, []byte(fmt.Sprintf("%d", os.Getpid())), 0o644); err != nil {
		t.Fatal(err)
	}
	if !synthdAlive(pidPath) {
		t.Fatal("current process PID must be alive")
	}
}

func TestListenerGateHoldsReadyClipAndEmitsIdle(t *testing.T) {
	home := t.TempDir()
	queue := filepath.Join(home, "queue")
	hls := filepath.Join(home, "hls")
	if err := os.MkdirAll(queue, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(hls, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_800_000_000, 0)
	clip := filepath.Join(queue, "100.wav")
	writeAt(t, clip, now)
	heartbeat := filepath.Join(hls, ".heartbeat")
	writeAt(t, heartbeat, now.Add(-11*time.Second))

	var calls [][]string
	w := writer{
		home: home, queue: queue, heartbeat: heartbeat,
		synthdPID: filepath.Join(home, ".synthd.pid"), idleFloor: "silence",
		stdout: io.Discard, now: func() time.Time { return now },
		processUp: func(string) bool { return false },
		runCommand: func(_ string, args []string, _ io.Writer) bool {
			calls = append(calls, append([]string(nil), args...))
			return true
		},
	}
	w.step()
	if _, err := os.Stat(clip); err != nil {
		t.Fatalf("dead listener must hold ready clip: %v", err)
	}
	if len(calls) != 1 || !contains(calls[0], "anullsrc=r=24000:cl=mono:d=0.25") {
		t.Fatalf("dead listener must emit one silence chunk, calls=%v", calls)
	}
}

func TestEmitUsesExactPrerollAndDecoderArguments(t *testing.T) {
	home := t.TempDir()
	clip := filepath.Join(home, "100.wav")
	caption := filepath.Join(home, "100.caption.json")
	if err := os.WriteFile(clip, []byte("audio"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(caption, []byte(`{"id":"100","text":"test"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	w := writer{
		home: home, stdout: io.Discard,
		runCommand: func(name string, args []string, _ io.Writer) bool {
			if name != "ffmpeg" {
				t.Fatalf("unexpected command %q", name)
			}
			calls = append(calls, append([]string(nil), args...))
			return true
		},
	}
	w.emitClip(clip)

	wantPreroll := []string{
		"-loglevel", "quiet", "-f", "lavfi", "-i", "anoisesrc=r=24000:c=pink:a=0.002:d=0.35",
		"-f", "s16le", "-ar", "24000", "-ac", "1", "-acodec", "pcm_s16le", "-",
	}
	wantDecode := []string{
		"-nostdin", "-loglevel", "quiet", "-i", clip,
		"-f", "s16le", "-ar", "24000", "-ac", "1", "-acodec", "pcm_s16le", "-",
	}
	if len(calls) != 2 || !reflect.DeepEqual(calls[0], wantPreroll) || !reflect.DeepEqual(calls[1], wantDecode) {
		t.Fatalf("ffmpeg args diverged:\n got %v\nwant %v", calls, [][]string{wantPreroll, wantDecode})
	}
	if _, err := os.Stat(clip); !os.IsNotExist(err) {
		t.Fatalf("clip must be consumed, stat err=%v", err)
	}
	if _, err := os.Stat(caption); !os.IsNotExist(err) {
		t.Fatalf("caption must be consumed, stat err=%v", err)
	}
}

func TestIdleArgumentsMatchNoiseAndSilence(t *testing.T) {
	for _, tc := range []struct {
		floor  string
		source string
	}{
		{floor: "noise", source: "anoisesrc=r=24000:c=pink:a=0.002:d=0.25"},
		{floor: "silence", source: "anullsrc=r=24000:cl=mono:d=0.25"},
		{floor: "unknown", source: "anoisesrc=r=24000:c=pink:a=0.002:d=0.25"},
	} {
		t.Run(tc.floor, func(t *testing.T) {
			var got []string
			w := writer{
				idleFloor: tc.floor, stdout: io.Discard,
				runCommand: func(name string, args []string, _ io.Writer) bool {
					if name != "ffmpeg" {
						t.Fatalf("unexpected command %q", name)
					}
					got = append([]string(nil), args...)
					return true
				},
			}
			w.emitIdle()
			want := []string{
				"-loglevel", "quiet", "-f", "lavfi", "-i", tc.source,
				"-f", "s16le", "-ar", "24000", "-ac", "1", "-acodec", "pcm_s16le", "-",
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("idle ffmpeg args:\n got %v\nwant %v", got, want)
			}
		})
	}
}

func writeAt(t *testing.T, path string, when time.Time) {
	t.Helper()
	if err := os.WriteFile(path, []byte("test"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}

func removeSuffix(t *testing.T, dir, suffix string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if filepath.Ext(entry.Name()) == suffix {
			if err := os.Remove(filepath.Join(dir, entry.Name())); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func down() bool { return false }

func up() bool { return true }
