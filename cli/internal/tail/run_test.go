package tail

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func dataExtFor(stamp int64) string {
	if stamp%2 == 0 {
		return ".wav"
	}
	return ".txt"
}

// seedBlock writes one queued block for `session`: a data file (.txt/.wav) plus
// its caption sidecar carrying the session id.
func seedBlock(t *testing.T, q string, stamp int64, session string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(q, strconv.FormatInt(stamp, 10)+dataExtFor(stamp)), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	cap, _ := json.Marshal(map[string]string{"session_id": session})
	if err := os.WriteFile(filepath.Join(q, strconv.FormatInt(stamp, 10)+".caption.json"), cap, 0o644); err != nil {
		t.Fatal(err)
	}
}

func present(q string, stamp int64) bool {
	_, err := os.Stat(filepath.Join(q, strconv.FormatInt(stamp, 10)+dataExtFor(stamp)))
	return err == nil
}

func newQueue(t *testing.T) (home, q string) {
	t.Helper()
	home = t.TempDir()
	q = filepath.Join(home, "queue")
	if err := os.MkdirAll(q, 0o755); err != nil {
		t.Fatal(err)
	}
	return home, q
}

// Switching channels must cut the old session's queued audio immediately.
func TestPruneQueue_DropsOtherSessions(t *testing.T) {
	home, q := newQueue(t)
	seedBlock(t, q, 100, "S")
	seedBlock(t, q, 101, "OTHER")
	seedBlock(t, q, 102, "S")
	seedBlock(t, q, 103, "OTHER")

	pruneQueue(home, "S", 10) // generous keep — only the session filter should act

	if !present(q, 100) || !present(q, 102) {
		t.Fatalf("selected-session blocks must survive")
	}
	if present(q, 101) || present(q, 103) {
		t.Fatalf("blocks from a switched-away session must be dropped")
	}
	if _, err := os.Stat(filepath.Join(q, "101.caption.json")); err == nil {
		t.Fatalf("a dropped block's caption sidecar should be removed too")
	}
}

// Within the selected session, keep only the newest `keep`; drop the stale middle.
func TestPruneQueue_CapsSelectedBacklog(t *testing.T) {
	home, q := newQueue(t)
	for s := int64(200); s <= 206; s++ { // 7 blocks, all session S
		seedBlock(t, q, s, "S")
	}
	pruneQueue(home, "S", 3)
	for s := int64(200); s <= 203; s++ {
		if present(q, s) {
			t.Fatalf("stale block %d should be dropped", s)
		}
	}
	for s := int64(204); s <= 206; s++ {
		if !present(q, s) {
			t.Fatalf("newest block %d should be kept", s)
		}
	}
	// idempotent at/under keep
	pruneQueue(home, "S", 3)
	kept := 0
	for s := int64(204); s <= 206; s++ {
		if present(q, s) {
			kept++
		}
	}
	if kept != 3 {
		t.Fatalf("second prune should be a no-op; kept %d want 3", kept)
	}
}

// With nothing selected, never nuke a session's audio.
func TestPruneQueue_NoSelectionKeepsSessions(t *testing.T) {
	home, q := newQueue(t)
	seedBlock(t, q, 300, "A")
	seedBlock(t, q, 301, "B")
	pruneQueue(home, "", 10)
	if !present(q, 300) || !present(q, 301) {
		t.Fatalf("with no selection, session-based dropping must not happen")
	}
}
