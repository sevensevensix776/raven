// Package rlog appends structured events to logs/events.jsonl — the single log
// the Go commands and the Python synthd share, and the one `raven diagnose`
// reads. One JSON object per line: {ts, comp, event, ...fields}.
package rlog

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"
)

// Log appends {ts, comp, event, ...fields}. Never returns an error to the
// caller — logging must never break the hook (matches ravenlog's fail-soft).
func Log(home, comp, event string, fields map[string]any) {
	dir := filepath.Join(home, "logs")
	_ = os.MkdirAll(dir, 0o755)

	rec := map[string]any{
		"ts":    float64(time.Now().UnixNano()) / 1e9,
		"comp":  comp,
		"event": event,
	}
	for k, v := range fields {
		if v != nil {
			rec[k] = v
		}
	}
	b, err := json.Marshal(rec)
	if err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "events.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(b, '\n'))
}
