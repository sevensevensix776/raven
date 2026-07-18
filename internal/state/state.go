// Package state ports the channels.json + selection.json registry logic from
// speak-reply.sh's Python block, under the same .state.lock flock so a phone
// pin and a UserPromptSubmit can't tear each other's state. Output JSON is
// byte-compatible with the Python writer (same keys, order, compact form).
package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

type Recent struct {
	Text string  `json:"text"`
	At   float64 `json:"at"`
}

type Channel struct {
	SessionID       string   `json:"session_id"`
	Project         string   `json:"project"`
	LastActiveEpoch float64  `json:"last_active_epoch"`
	LastLine        string   `json:"last_line"`
	Recent          []Recent `json:"recent"`
}

type Selection struct {
	Mode            string  `json:"mode"`
	SessionID       *string `json:"session_id"`
	FollowSessionID *string `json:"follow_session_id"`
}

// UpdateRegistry mirrors the Python registry block exactly, including the order
// of operations (SessionEnd unsticks selection before `pinned` is computed; the
// UserPromptSubmit follow-write happens last).
func UpdateRegistry(home, event, session, cwd, lastLine string, ttlHours float64) {
	lock, err := os.OpenFile(filepath.Join(home, ".state.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return
	}
	defer lock.Close()
	if syscall.Flock(int(lock.Fd()), syscall.LOCK_EX) != nil {
		return
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)

	now := float64(time.Now().UnixNano()) / 1e9
	sel := readSelection(home)
	existing := readChannels(home)

	// Carry the session's rolling reply history across the row rebuild.
	recent := []Recent{}
	rest := existing[:0:0]
	for _, c := range existing {
		if c.SessionID == session {
			if c.Recent != nil {
				recent = c.Recent
			}
			continue
		}
		rest = append(rest, c)
	}
	channels := rest

	if event == "SessionEnd" {
		if sel.FollowSessionID != nil && *sel.FollowSessionID == session {
			sel.FollowSessionID = nil
		}
		if sel.SessionID != nil && *sel.SessionID == session {
			sel.Mode = "follow"
			sel.SessionID = sel.FollowSessionID
		}
		writeJSON(filepath.Join(home, "selection.json"), sel)
	} else {
		if event == "Stop" && strings.TrimSpace(lastLine) != "" {
			recent = append(recent, Recent{Text: lastLine, At: now})
			if len(recent) > 3 {
				recent = recent[len(recent)-3:]
			}
		}
		project := ""
		if cwd != "-" {
			project = filepath.Base(cwd)
		}
		channels = append(channels, Channel{
			SessionID:       session,
			Project:         project,
			LastActiveEpoch: now,
			LastLine:        lastLine,
			Recent:          recent, // never nil -> marshals as [], not null
		})
	}

	// TTL backstop + pinned retention. `pinned` reflects sel AFTER SessionEnd.
	pinned := ""
	if sel.Mode == "pinned" && sel.SessionID != nil {
		pinned = *sel.SessionID
	}
	cutoff := now - ttlHours*3600
	kept := channels[:0:0]
	for _, c := range channels {
		if c.LastActiveEpoch >= cutoff || c.SessionID == pinned {
			kept = append(kept, c)
		}
	}
	sort.SliceStable(kept, func(i, j int) bool {
		return kept[i].LastActiveEpoch > kept[j].LastActiveEpoch
	})
	if len(kept) > 50 {
		kept = kept[:50]
	}
	writeJSON(filepath.Join(home, "channels.json"), kept)

	if event == "UserPromptSubmit" {
		s := session
		sel.FollowSessionID = &s
		if sel.Mode == "" || sel.Mode == "follow" {
			sel.SessionID = &s
		}
		writeJSON(filepath.Join(home, "selection.json"), sel)
	}
}

// SelectedSession returns selection.json's active session_id ("" if none) —
// used by the speech gate and the user-transcript gate.
func SelectedSession(home string) string {
	sel := readSelection(home)
	if sel.SessionID == nil {
		return ""
	}
	return *sel.SessionID
}

func readSelection(home string) Selection {
	sel := Selection{Mode: "follow"}
	b, err := os.ReadFile(filepath.Join(home, "selection.json"))
	if err == nil {
		_ = json.Unmarshal(b, &sel)
	}
	return sel
}

func readChannels(home string) []Channel {
	var cs []Channel
	b, err := os.ReadFile(filepath.Join(home, "channels.json"))
	if err == nil {
		_ = json.Unmarshal(b, &cs)
	}
	return cs
}

func writeJSON(path string, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		return
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return
	}
	tmp.Sync()
	tmp.Close()
	if os.Rename(tmpName, path) != nil {
		os.Remove(tmpName)
	}
}
