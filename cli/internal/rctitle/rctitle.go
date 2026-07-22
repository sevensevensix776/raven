// Package rctitle reads a Claude Code session's Remote Control name from its
// transcript JSONL: the last `customTitle` (user-set) wins, else the last
// `aiTitle`. Used by the hook (on each fire) and by the server (to freshen names
// for sessions that were renamed while idle).
package rctitle

import (
	"bufio"
	"bytes"
	"encoding/json"
	"os"
	"sync"
)

// Read scans the transcript for the last customTitle (else aiTitle). "" if the
// path is empty or unreadable.
func Read(path string) string {
	if path == "" {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)
	var custom, ai string
	for sc.Scan() {
		line := sc.Bytes()
		if bytes.Contains(line, []byte(`"customTitle"`)) {
			var e struct {
				CustomTitle string `json:"customTitle"`
			}
			if json.Unmarshal(line, &e) == nil && e.CustomTitle != "" {
				custom = e.CustomTitle
			}
		} else if bytes.Contains(line, []byte(`"aiTitle"`)) {
			var e struct {
				AiTitle string `json:"aiTitle"`
			}
			if json.Unmarshal(line, &e) == nil && e.AiTitle != "" {
				ai = e.AiTitle
			}
		}
	}
	if custom != "" {
		return custom
	}
	return ai
}

// Resolver caches titles by (path, mtime) so the server can freshen names on
// every /channels request without re-scanning unchanged transcripts. A rename
// appends a title entry, bumping mtime, so the next Resolve picks it up once.
type Resolver struct {
	mu    sync.Mutex
	cache map[string]entry
}

type entry struct {
	mtime int64
	title string
}

func NewResolver() *Resolver { return &Resolver{cache: map[string]entry{}} }

// Resolve returns the session name for a transcript path, reading it only when
// the file's mtime has changed since the last read.
func (r *Resolver) Resolve(path string) string {
	if path == "" {
		return ""
	}
	fi, err := os.Stat(path)
	if err != nil {
		return ""
	}
	mtime := fi.ModTime().UnixNano()

	r.mu.Lock()
	if e, ok := r.cache[path]; ok && e.mtime == mtime {
		r.mu.Unlock()
		return e.title
	}
	r.mu.Unlock()

	title := Read(path)

	r.mu.Lock()
	r.cache[path] = entry{mtime: mtime, title: title}
	r.mu.Unlock()
	return title
}
