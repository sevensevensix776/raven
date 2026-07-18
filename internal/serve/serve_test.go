package serve

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTranscriptLinesPreservesAdditiveFields(t *testing.T) {
	home := t.TempDir()
	spoken := `{"id":"new","text":"spoken","display":"**Readable**\n\n- item"}` + "\n"
	if err := os.WriteFile(filepath.Join(home, "spoken.jsonl"), []byte(spoken), 0o644); err != nil {
		t.Fatal(err)
	}

	lines := transcriptLines(home, 50)
	if len(lines) != 1 {
		t.Fatalf("got %d transcript lines, want 1", len(lines))
	}
	var line map[string]any
	if err := json.Unmarshal(lines[0], &line); err != nil {
		t.Fatal(err)
	}
	if line["display"] != "**Readable**\n\n- item" {
		t.Fatalf("serve dropped display: %#v", line)
	}
}

func TestETag(t *testing.T) {
	got := etag([]byte("abc"))
	want := `"ba7816bf8f01cfea4141"`
	if got != want {
		t.Fatalf("etag(abc) = %s, want %s", got, want)
	}
}

func TestHealthSnapshot(t *testing.T) {
	home := t.TempDir()
	for _, dir := range []string{"hls", "queue"} {
		if err := os.MkdirAll(filepath.Join(home, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Unix(1_000, 200_000_000)
	heartbeat := filepath.Join(home, "hls", ".heartbeat")
	if err := os.WriteFile(heartbeat, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	hbTime := now.Add(-5200 * time.Millisecond)
	if err := os.Chtimes(heartbeat, hbTime, hbTime); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"one.txt", "two.txt", "one.wav", "one.aiff"} {
		if err := os.WriteFile(filepath.Join(home, "queue", name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(home, "selection.json"), []byte(`{"mode":"pinned","session_id":"session-b","follow_session_id":"session-a"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "channels.json"), []byte(`[{"session_id":"session-a"},{"session_id":"session-b"}]`), 0o644); err != nil {
		t.Fatal(err)
	}
	longText := strings.Repeat("é", 121)
	spoken := `{"id":"old","text":"old"}` + "\n" + `{"id":"new","text":"` + longText + `","role":"claude"}` + "\n"
	if err := os.WriteFile(filepath.Join(home, "spoken.jsonl"), []byte(spoken), 0o644); err != nil {
		t.Fatal(err)
	}

	snapshot := healthSnapshot(home, now)
	if snapshot.TS != 1000.2 {
		t.Errorf("ts = %v, want 1000.2", snapshot.TS)
	}
	if snapshot.HeartbeatAgeS == nil || *snapshot.HeartbeatAgeS != 5.2 {
		t.Errorf("heartbeat_age_s = %v, want 5.2", snapshot.HeartbeatAgeS)
	}
	if !snapshot.ListenerLive {
		t.Error("listener should be live at 5.2 seconds")
	}
	if snapshot.QueuePending != (queuePending{Txt: 2, Wav: 1, Aiff: 1}) {
		t.Errorf("queue_pending = %#v", snapshot.QueuePending)
	}
	if snapshot.Selection.Mode != "pinned" || snapshot.Selection.SessionID != "session-b" {
		t.Errorf("selection = %#v", snapshot.Selection)
	}
	if snapshot.Channels != 2 {
		t.Errorf("channels = %d, want 2", snapshot.Channels)
	}
	if snapshot.LastSpoken["chars"] != 121 {
		t.Errorf("last_spoken chars = %v, want 121", snapshot.LastSpoken["chars"])
	}
	if got, _ := snapshot.LastSpoken["text"].(string); len([]rune(got)) != 120 {
		t.Errorf("last_spoken text has %d runes, want 120", len([]rune(got)))
	}
}
