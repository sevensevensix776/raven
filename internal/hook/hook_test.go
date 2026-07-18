package hook

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// setup makes a throwaway RAVEN_HOME and points the hook at it.
func setup(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	os.MkdirAll(filepath.Join(dir, "queue"), 0o755)
	os.WriteFile(filepath.Join(dir, "config.sh"), []byte("MAX_SPOKEN_CHARS=0\nCHANNEL_TTL_HOURS=6\n"), 0o644)
	t.Setenv("RAVEN_HOME", dir)
	return dir
}

func fire(t *testing.T, payload map[string]any) {
	t.Helper()
	b, _ := json.Marshal(payload)
	Run(strings.NewReader(string(b)))
}

func readJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
}

func TestFollowActivatesAndQueues(t *testing.T) {
	dir := setup(t)
	fire(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "A", "cwd": "/x/cerebro-api", "prompt": "fix it"})
	fire(t, map[string]any{"hook_event_name": "Stop", "session_id": "A", "cwd": "/x/cerebro-api", "last_assistant_message": "Done, tests pass."})

	var sel map[string]any
	readJSON(t, filepath.Join(dir, "selection.json"), &sel)
	if sel["session_id"] != "A" {
		t.Errorf("expected session A selected, got %v", sel["session_id"])
	}
	txts, _ := filepath.Glob(filepath.Join(dir, "queue", "*.txt"))
	if len(txts) != 1 {
		t.Fatalf("expected 1 queued .txt, got %d", len(txts))
	}
	body, _ := os.ReadFile(txts[0])
	if !strings.Contains(string(body), "In cerebro-api. Done, tests pass.") {
		t.Errorf("queued text wrong: %q", body)
	}
}

func TestGateSkipsOtherSession(t *testing.T) {
	dir := setup(t)
	os.WriteFile(filepath.Join(dir, "selection.json"),
		[]byte(`{"mode":"pinned","session_id":"A","follow_session_id":"A"}`), 0o644)
	fire(t, map[string]any{"hook_event_name": "Stop", "session_id": "B", "cwd": "/x/forge", "last_assistant_message": "should not queue"})

	txts, _ := filepath.Glob(filepath.Join(dir, "queue", "*.txt"))
	if len(txts) != 0 {
		t.Errorf("non-selected session must not queue, got %d", len(txts))
	}
}

func TestSessionEndRemovesAndUnsticks(t *testing.T) {
	dir := setup(t)
	os.WriteFile(filepath.Join(dir, "selection.json"),
		[]byte(`{"mode":"pinned","session_id":"Y","follow_session_id":"X"}`), 0o644)
	os.WriteFile(filepath.Join(dir, "channels.json"),
		[]byte(`[{"session_id":"Y","project":"f","last_active_epoch":9e12,"last_line":"","recent":[]}]`), 0o644)
	fire(t, map[string]any{"hook_event_name": "SessionEnd", "session_id": "Y", "cwd": "/x/f", "reason": "clear"})

	var chans []map[string]any
	readJSON(t, filepath.Join(dir, "channels.json"), &chans)
	for _, c := range chans {
		if c["session_id"] == "Y" {
			t.Error("ended session Y must be removed")
		}
	}
	var sel map[string]any
	readJSON(t, filepath.Join(dir, "selection.json"), &sel)
	if sel["mode"] != "follow" {
		t.Errorf("selection must revert to follow, got %v", sel["mode"])
	}
}

func TestRecentIsNeverNull(t *testing.T) {
	dir := setup(t)
	fire(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "A", "cwd": "/x/api", "prompt": "hi"})
	b, _ := os.ReadFile(filepath.Join(dir, "channels.json"))
	// Must serialize recent as [] not null, or the server's /catchup crashes.
	if strings.Contains(string(b), `"recent":null`) {
		t.Errorf("recent must be [] not null: %s", b)
	}
}

func TestSystemInjectedNotRecordedAsUser(t *testing.T) {
	dir := setup(t)
	// A task-notification injected as a UserPromptSubmit must NOT hit the transcript.
	fire(t, map[string]any{
		"hook_event_name": "UserPromptSubmit", "session_id": "A", "cwd": "/x/api",
		"prompt": "<task-notification>\n<task-id>xyz</task-id>\n<status>completed</status>\n</task-notification>",
	})
	if b, err := os.ReadFile(filepath.Join(dir, "spoken.jsonl")); err == nil && len(b) > 0 {
		t.Errorf("system-injected prompt leaked into transcript: %s", b)
	}
	// A real prompt still records.
	fire(t, map[string]any{"hook_event_name": "UserPromptSubmit", "session_id": "A", "cwd": "/x/api", "prompt": "real user question"})
	b, _ := os.ReadFile(filepath.Join(dir, "spoken.jsonl"))
	if !strings.Contains(string(b), "real user question") {
		t.Errorf("real prompt should be recorded, got %q", b)
	}
}
