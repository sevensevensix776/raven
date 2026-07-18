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
	Name            string   `json:"name"` // Remote Control session title (custom/ai); "" if unknown
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
func UpdateRegistry(home, event, session, cwd, lastLine, name string, ttlHours float64) {
	unlock, err := Lock(home)
	if err != nil {
		return
	}
	defer unlock()

	now := float64(time.Now().UnixNano()) / 1e9
	sel := ReadSelection(home)
	existing := ReadChannels(home)

	// Carry the session's rolling reply history + Remote Control name across the
	// row rebuild. A freshly-read non-empty name wins; otherwise keep the prior.
	recent := []Recent{}
	priorName := ""
	priorLine := ""
	rest := existing[:0:0]
	for _, c := range existing {
		if c.SessionID == session {
			if c.Recent != nil {
				recent = c.Recent
			}
			priorName = c.Name
			priorLine = c.LastLine
			continue
		}
		rest = append(rest, c)
	}
	channels := rest
	if name == "" {
		name = priorName
	}
	if lastLine == "" {
		lastLine = priorLine // don't blank the preview on empty/system-injected events
	}

	if event == "SessionEnd" {
		if sel.FollowSessionID != nil && *sel.FollowSessionID == session {
			sel.FollowSessionID = nil
		}
		if sel.SessionID != nil && *sel.SessionID == session {
			sel.Mode = "follow"
			sel.SessionID = sel.FollowSessionID
		}
		_ = WriteJSON(filepath.Join(home, "selection.json"), sel)
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
			Name:            name,
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
	_ = WriteJSON(filepath.Join(home, "channels.json"), kept)

	if event == "UserPromptSubmit" {
		s := session
		sel.FollowSessionID = &s
		if sel.Mode == "" || sel.Mode == "follow" {
			sel.SessionID = &s
		}
		_ = WriteJSON(filepath.Join(home, "selection.json"), sel)
	}
}

// SelectedSession returns selection.json's active session_id ("" if none) —
// used by the speech gate and the user-transcript gate.
func SelectedSession(home string) string {
	sel := ReadSelection(home)
	if sel.SessionID == nil {
		return ""
	}
	return *sel.SessionID
}

// ReadSelection reads selection.json, returning follow mode with nil session
// fields when the file is absent or malformed.
func ReadSelection(home string) Selection {
	sel := Selection{Mode: "follow"}
	b, err := os.ReadFile(filepath.Join(home, "selection.json"))
	if err == nil {
		if json.Unmarshal(b, &sel) != nil {
			return Selection{Mode: "follow"}
		}
	}
	return sel
}

// ReadChannels reads channels.json, returning an empty (non-nil) slice when
// the file is absent or malformed so callers serialize [] rather than null.
func ReadChannels(home string) []Channel {
	cs := []Channel{}
	b, err := os.ReadFile(filepath.Join(home, "channels.json"))
	if err == nil {
		if json.Unmarshal(b, &cs) != nil {
			return []Channel{}
		}
	}
	return cs
}

// Lock takes Raven's cross-process state flock and returns an unlock function.
// The caller must invoke unlock when it has finished all related reads/writes.
func Lock(home string) (unlock func(), err error) {
	lock, err := os.OpenFile(filepath.Join(home, ".state.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		_ = lock.Close()
		return nil, err
	}
	return func() {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
	}, nil
}

// WriteJSON atomically writes compact JSON and fsyncs it before rename.
func WriteJSON(path string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(b); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		_ = os.Remove(tmpName)
		return err
	}
	return nil
}
