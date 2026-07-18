// Package transcript appends a user-prompt line to spoken.jsonl, matching
// transcript_user.py: role=user, screen-only (never spoken), last-200 kept,
// flocked on .transcript.lock so a prompt and a reply can't clobber the file.
package transcript

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const keep = 200

// AddUser appends the user's prompt (already whitespace-collapsed, 600-byte
// capped by the caller) to spoken.jsonl as a role=user entry.
func AddUser(home, session, project, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	entry := map[string]any{
		"id":              fmt.Sprintf("u%d", time.Now().UnixNano()),
		"session_id":      session,
		"project":         project,
		"text":            text,
		"role":            "user",
		"spoken_at_epoch": float64(time.Now().UnixNano()) / 1e9,
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return
	}

	lock, err := os.OpenFile(filepath.Join(home, ".transcript.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return
	}
	defer lock.Close()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX) != nil {
		return
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	spoken := filepath.Join(home, "spoken.jsonl")
	existing := tail(spoken, keep-1)
	existing = append(existing, string(line))

	tmp, err := os.CreateTemp(home, ".spoken.*")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(strings.Join(existing, "\n") + "\n"); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return
	}
	tmp.Sync()
	tmp.Close()
	if os.Rename(tmpName, spoken) != nil {
		os.Remove(tmpName)
	}
}

func tail(path string, n int) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	var lines []string
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		if t := sc.Text(); t != "" {
			lines = append(lines, t)
		}
	}
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return lines
}
