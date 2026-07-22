package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// Prompt watermarks answer one question: has the user spoken since this audio
// was generated?
//
// When you reply on a thread, the conversation has moved past everything Claude
// said before that reply — so speaking it afterwards is not just late, it is
// wrong. It narrates a state of the world you have already responded to. The
// watermark is the timestamp of your most recent turn in a session; any queued
// clip stamped earlier than it is superseded and must be dropped.
//
// This is deliberately independent of *how* you replied. The hook fires for a
// prompt typed at the terminal and for one dictated through Remote Control
// alike, so both silence stale narration.

const watermarkFile = "watermarks.json"

// watermarkKeep bounds the file so a long-lived home does not accumulate an
// entry per session forever. Sessions are pruned oldest-first.
const watermarkKeep = 64

func watermarkPath(home string) string { return filepath.Join(home, watermarkFile) }

func readWatermarks(home string) map[string]int64 {
	m := map[string]int64{}
	b, err := os.ReadFile(watermarkPath(home))
	if err != nil {
		return m
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return map[string]int64{}
	}
	return m
}

// SetPromptWatermark records that the user just spoke in session, returning the
// nanosecond timestamp written. Callers should hold the state lock. Failures are
// silent: a missed watermark means stale narration is merely not suppressed,
// which must never be allowed to block a Claude Code turn.
func SetPromptWatermark(home, session string) int64 {
	if session == "" {
		return 0
	}
	now := time.Now().UnixNano()
	m := readWatermarks(home)
	m[session] = now
	if len(m) > watermarkKeep {
		type kv struct {
			k string
			v int64
		}
		all := make([]kv, 0, len(m))
		for k, v := range m {
			all = append(all, kv{k, v})
		}
		sort.Slice(all, func(i, j int) bool { return all[i].v > all[j].v })
		m = map[string]int64{}
		for _, e := range all[:watermarkKeep] {
			m[e.k] = e.v
		}
	}
	_ = WriteJSON(watermarkPath(home), m)
	return now
}

// PromptWatermark returns the nanosecond timestamp of the user's most recent
// turn in session, or 0 if they have not spoken. Reads are lock-free: WriteJSON
// renames atomically, so a concurrent reader sees either the whole previous file
// or the whole new one.
func PromptWatermark(home, session string) int64 {
	if session == "" {
		return 0
	}
	return readWatermarks(home)[session]
}
