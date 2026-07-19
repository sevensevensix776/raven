package tail

import (
	"bytes"
	"encoding/json"
	"testing"
)

func jstr(s string) string { b, _ := json.Marshal(s); return string(b) }

// asstLine builds an assistant JSONL record. Each block is {type, text}.
func asstLine(uuid string, blocks ...[2]string) string {
	parts := ""
	for i, b := range blocks {
		if i > 0 {
			parts += ","
		}
		parts += `{"type":` + jstr(b[0]) + `,"text":` + jstr(b[1]) + `}`
	}
	return `{"type":"assistant","uuid":` + jstr(uuid) + `,"message":{"content":[` + parts + `]}}`
}

func TestParseNewBlocks_EligibilityOrderingAndPartial(t *testing.T) {
	sess := "sess-1"
	var buf bytes.Buffer
	buf.WriteString(asstLine("u1", [2]string{"text", "First reply."}) + "\n")
	buf.WriteString(asstLine("u2", [2]string{"thinking", "secret reasoning"}) + "\n")
	buf.WriteString(`{"type":"user","uuid":"u3","message":{"content":[{"type":"text","text":"you talking"}]}}` + "\n")
	buf.WriteString(asstLine("u4", [2]string{"tool_use", "Bash"}) + "\n")
	// multi-block entry: one text (eligible) + one thinking (skipped)
	buf.WriteString(asstLine("u5", [2]string{"text", "Second reply."}, [2]string{"thinking", "more reasoning"}) + "\n")
	// a text block that cleans to nothing (pure fenced code) — must be skipped
	buf.WriteString(asstLine("u6", [2]string{"text", "```\nfmt.Println(1)\n```"}) + "\n")
	partial := `{"type":"assistant","uuid":"u7","message":{"content":[{"type":"text","tex`
	buf.WriteString(partial) // no trailing newline

	data := buf.Bytes()
	seen := map[string]struct{}{}
	blocks, consumed := ParseNewBlocks(data, 0, sess, seen, 0)

	if len(blocks) != 2 {
		t.Fatalf("expected 2 eligible blocks, got %d: %+v", len(blocks), texts(blocks))
	}
	if blocks[0].Cleaned != "First reply." || blocks[1].Cleaned != "Second reply." {
		t.Fatalf("wrong text/order: %v", texts(blocks))
	}
	if blocks[1].Index != 0 {
		t.Fatalf("multi-block text should be index 0, got %d", blocks[1].Index)
	}
	// partial trailing line must be left unconsumed for the next read
	if int(consumed) != len(data)-len(partial) {
		t.Fatalf("consumed=%d want=%d (partial line must not be consumed)", consumed, len(data)-len(partial))
	}
	if got := string(data[consumed:]); got != partial {
		t.Fatalf("leftover=%q want partial=%q", got, partial)
	}
	// LineEnd of the first block points just past its newline
	if data[blocks[0].LineEnd-1] != '\n' {
		t.Fatalf("LineEnd should sit just past a newline")
	}
	// distinct text -> distinct keys and hashes
	if blocks[0].Key == blocks[1].Key || blocks[0].TextHash == blocks[1].TextHash {
		t.Fatalf("distinct blocks must have distinct keys/hashes")
	}
}

func TestParseNewBlocks_DedupAcrossReads(t *testing.T) {
	sess := "sess-1"
	line := asstLine("u1", [2]string{"text", "Only once."}) + "\n"
	data := []byte(line)
	seen := map[string]struct{}{}

	first, _ := ParseNewBlocks(data, 0, sess, seen, 0)
	if len(first) != 1 {
		t.Fatalf("first pass expected 1 block, got %d", len(first))
	}
	// caller records the key after commit
	seen[first[0].Key] = struct{}{}

	// re-scanning the same data (e.g. after a restart re-reading the line) yields nothing
	again, _ := ParseNewBlocks(data, 0, sess, seen, 0)
	if len(again) != 0 {
		t.Fatalf("seen block must not reappear, got %d", len(again))
	}
}

func TestParseNewBlocks_IdenticalTextDistinctEntriesBothSpeak(t *testing.T) {
	sess := "sess-1"
	// Same text, different uuids (two legitimate identical replies) -> both eligible.
	var buf bytes.Buffer
	buf.WriteString(asstLine("uA", [2]string{"text", "Done."}) + "\n")
	buf.WriteString(asstLine("uB", [2]string{"text", "Done."}) + "\n")
	blocks, _ := ParseNewBlocks(buf.Bytes(), 0, sess, map[string]struct{}{}, 0)
	if len(blocks) != 2 {
		t.Fatalf("identical text at distinct entries should both speak, got %d", len(blocks))
	}
	if blocks[0].Key == blocks[1].Key {
		t.Fatalf("distinct entries must have distinct keys even with identical text")
	}
}

func TestParseNewBlocks_NoNewlineNoConsume(t *testing.T) {
	sess := "sess-1"
	data := []byte(asstLine("u1", [2]string{"text", "Incomplete"})) // no newline
	blocks, consumed := ParseNewBlocks(data, 0, sess, map[string]struct{}{}, 0)
	if len(blocks) != 0 || consumed != 0 {
		t.Fatalf("an unterminated line must yield no blocks and consume 0; got blocks=%d consumed=%d", len(blocks), consumed)
	}
}

func texts(bs []Block) []string {
	out := make([]string, len(bs))
	for i, b := range bs {
		out[i] = b.Cleaned
	}
	return out
}
