package transcript

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestAddClaudeFromCaption(t *testing.T) {
	home := t.TempDir()
	caption := filepath.Join(home, "123.caption.json")
	if err := os.WriteFile(caption, []byte(`{"session_id":"s","project":"p","text":"hello"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	AddClaude(home, caption)

	f, err := os.Open(filepath.Join(home, "spoken.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if !bufio.NewScanner(f).Scan() {
		t.Fatal("expected one transcript line")
	}
	b, err := os.ReadFile(filepath.Join(home, "spoken.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var entry map[string]any
	if err := json.Unmarshal(b, &entry); err != nil {
		t.Fatal(err)
	}
	if entry["id"] != "123" || entry["role"] != "claude" || entry["text"] != "hello" {
		t.Fatalf("unexpected Claude entry: %#v", entry)
	}
	if _, ok := entry["spoken_at_epoch"].(float64); !ok {
		t.Fatalf("spoken_at_epoch missing: %#v", entry)
	}
}

func TestAddClaudeIgnoresMalformedCaption(t *testing.T) {
	home := t.TempDir()
	caption := filepath.Join(home, "bad.caption.json")
	if err := os.WriteFile(caption, []byte("{"), 0o644); err != nil {
		t.Fatal(err)
	}
	AddClaude(home, caption)
	if _, err := os.Stat(filepath.Join(home, "spoken.jsonl")); !os.IsNotExist(err) {
		t.Fatalf("malformed caption must not append, stat err=%v", err)
	}
}
