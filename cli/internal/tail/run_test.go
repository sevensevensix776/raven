package tail

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestPruneQueue_KeepsNewestDropsStaleMiddle(t *testing.T) {
	home := t.TempDir()
	q := filepath.Join(home, "queue")
	if err := os.MkdirAll(q, 0o755); err != nil {
		t.Fatal(err)
	}
	// 7 queued blocks, stamps 100..106; alternate awaiting-synth (.txt) and
	// awaiting-play (.wav), each with a caption sidecar.
	write := func(stamp int64, ext string) {
		os.WriteFile(filepath.Join(q, strconv.FormatInt(stamp, 10)+ext), []byte("x"), 0o644)
	}
	dataExt := func(stamp int64) string {
		if stamp%2 == 0 {
			return ".wav"
		}
		return ".txt"
	}
	for s := int64(100); s <= 106; s++ {
		write(s, dataExt(s))
		write(s, ".caption.json")
	}

	pruneQueue(home, 3)

	// oldest four stamps (100..103) fully removed
	for s := int64(100); s <= 103; s++ {
		for _, ext := range []string{".txt", ".wav", ".aiff", ".caption.json"} {
			if _, err := os.Stat(filepath.Join(q, strconv.FormatInt(s, 10)+ext)); err == nil {
				t.Fatalf("stamp %d%s should have been pruned", s, ext)
			}
		}
	}
	// newest three stamps (104..106) survive
	for s := int64(104); s <= 106; s++ {
		if _, err := os.Stat(filepath.Join(q, strconv.FormatInt(s, 10)+dataExt(s))); err != nil {
			t.Fatalf("stamp %d should have been kept", s)
		}
	}

	// idempotent: at or under keep, prune is a no-op
	pruneQueue(home, 3)
	remaining := 0
	for s := int64(104); s <= 106; s++ {
		if _, err := os.Stat(filepath.Join(q, strconv.FormatInt(s, 10)+dataExt(s))); err == nil {
			remaining++
		}
	}
	if remaining != 3 {
		t.Fatalf("second prune should be a no-op; kept %d want 3", remaining)
	}
}
