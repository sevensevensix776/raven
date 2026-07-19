// Package tail extracts completed assistant text blocks from a Claude Code
// session transcript (JSONL) for live narration — speaking finished blocks while
// the same turn continues through tool calls, before the Stop hook fires.
//
// Claude Code appends one content block per JSONL line, so a newline-terminated
// assistant line whose block is type=text is a completed, speakable unit. This
// package is pure and testable: it turns a byte slice of transcript data into
// eligible blocks plus a cursor. All file I/O, polling, selection gating, and
// (later) queue commits live in the tail command.
package tail

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strconv"

	"raven-go/internal/clean"
)

// Block is one completed assistant text block eligible for narration.
type Block struct {
	Key       string // stable dedup identity: session+uuid+index+hash(raw)
	TextHash  string // sha256 of the cleaned spoken text, for Stop coordination
	Raw       string // raw block text as written to the transcript
	Cleaned   string // clean.Reply output — what would be spoken (no project prefix)
	UUID      string // transcript entry uuid
	Index     int    // content-block index within the entry
	Timestamp string // ISO8601 timestamp from the entry
	LineEnd   int64  // absolute byte offset just past this entry's line
}

// entry is the minimal shape of a transcript JSONL record. Only the fields the
// tailer needs are decoded; Claude Code writes many more.
type entry struct {
	Type      string `json:"type"`
	UUID      string `json:"uuid"`
	Timestamp string `json:"timestamp"`
	Message   struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
	} `json:"message"`
}

func sha(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// blockKey distinguishes two legitimately identical replies (different uuid or
// index) so both are spoken, while collapsing a resume/rewrite of the same entry
// (same uuid+index+text) into one — favoring an occasional skipped duplicate
// over systematic double-speak, per the live-narration design.
func blockKey(sessionID, uuid string, index int, raw string) string {
	return sha(sessionID + "\x00" + uuid + "\x00" + strconv.Itoa(index) + "\x00" + sha(raw))
}

// ParseNewBlocks scans complete JSONL lines in data (which begins at absolute
// file offset base) and returns, in transcript order, the eligible assistant
// text blocks for sessionID whose Key is not in seen — plus consumed, the number
// of bytes up to and including the last newline.
//
// It never advances past an unterminated trailing line (that partial record is
// left for the next read). It does not mutate seen; the caller records keys only
// after a successful commit, so a failed commit is retried. maxChars is the
// spoken byte cap passed through to clean.Reply (<= 0 means no cap).
func ParseNewBlocks(data []byte, base int64, sessionID string, seen map[string]struct{}, maxChars int) (blocks []Block, consumed int64) {
	batch := make(map[string]struct{}) // guards duplicates within this pass
	start := 0
	for i := 0; i < len(data); i++ {
		if data[i] != '\n' {
			continue
		}
		line := data[start:i]
		lineEnd := base + int64(i) + 1
		consumed = int64(i) + 1
		start = i + 1
		if len(line) == 0 {
			continue
		}
		var e entry
		if json.Unmarshal(line, &e) != nil {
			continue // skip malformed lines rather than stall the tailer
		}
		if e.Type != "assistant" {
			continue
		}
		for idx, b := range e.Message.Content {
			if b.Type != "text" || clean.IsBlank(b.Text) {
				continue // never speak thinking, tool_use, or blank text
			}
			cleaned := clean.Reply(b.Text, maxChars)
			if clean.IsBlank(cleaned) {
				continue // cleaning stripped it to nothing (e.g. pure code)
			}
			key := blockKey(sessionID, e.UUID, idx, b.Text)
			if _, ok := seen[key]; ok {
				continue
			}
			if _, ok := batch[key]; ok {
				continue
			}
			batch[key] = struct{}{}
			blocks = append(blocks, Block{
				Key: key, TextHash: sha(cleaned), Raw: b.Text, Cleaned: cleaned,
				UUID: e.UUID, Index: idx, Timestamp: e.Timestamp, LineEnd: lineEnd,
			})
		}
	}
	return blocks, consumed
}
