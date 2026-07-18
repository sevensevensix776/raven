// Package hook ports speak-reply.sh: read a Claude Code hook payload on stdin,
// maintain the channel registry, and (for the selected channel's Stop) queue the
// reply for synthesis. Pure Go, no subprocesses — the whole point of the port.
package hook

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"raven-go/internal/clean"
	"raven-go/internal/config"
	"raven-go/internal/rlog"
	"raven-go/internal/state"
	"raven-go/internal/transcript"
)

type payload struct {
	Event   string `json:"hook_event_name"`
	Session string `json:"session_id"`
	Cwd     string `json:"cwd"`
	Message string `json:"last_assistant_message"`
	Prompt  string `json:"prompt"`
}

type caption struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	Project   string `json:"project"`
	Text      string `json:"text"`
}

// Home resolves the Raven dir: RAVEN_HOME override (for tests) else ~/speech.
func Home() string {
	if h := os.Getenv("RAVEN_HOME"); h != "" {
		return h
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "speech")
}

// Run executes the hook against stdin. Always returns nil exit intent — the hook
// must never fail a Claude turn; errors are swallowed like the bash `2>/dev/null`.
func Run(stdin io.Reader) {
	home := Home()
	if fi, err := os.Stat(home); err != nil || !fi.IsDir() {
		return
	}
	cfg := config.Load(home)
	_ = os.MkdirAll(filepath.Join(home, "queue"), 0o755)

	raw, _ := io.ReadAll(stdin)
	var p payload
	if json.Unmarshal(raw, &p) != nil {
		return
	}
	event := dashDefault(p.Event)
	session := dashDefault(p.Session)
	cwd := dashDefault(p.Cwd)
	rawText := p.Message
	if rawText == "" {
		rawText = p.Prompt
	}

	registryLine := clean.Collapse(rawText, 280)
	state.UpdateRegistry(home, event, session, cwd, registryLine, cfg.ChannelTTLHours)

	switch event {
	case "UserPromptSubmit":
		// Record the prompt in the transcript (screen only) for the selected
		// channel — the registry above already made it active in follow mode.
		if speakAll(home) || session == state.SelectedSession(home) {
			userText := strings.Join(strings.Fields(rawText), " ")
			if len(userText) > 600 {
				userText = userText[:600]
			}
			transcript.AddUser(home, session, projectName(cwd), userText)
		}
		return
	case "Stop":
		// fall through to speech
	default:
		return
	}

	// Speech gate: only the selected channel is spoken.
	if !speakAll(home) {
		selected := state.SelectedSession(home)
		if selected == "" || session != selected {
			rlog.Log(home, "hook", "gate_skip", map[string]any{
				"session": session, "selected": selected, "project": projectName(cwd),
			})
			return
		}
	}

	if clean.IsBlank(rawText) {
		return
	}
	cleaned := clean.Reply(rawText, cfg.MaxSpokenChars)
	if clean.IsBlank(cleaned) {
		return
	}

	project := projectName(cwd)
	stamp := strconv.FormatInt(time.Now().UnixNano(), 10)
	spoken := cleaned
	if project != "" {
		spoken = "In " + project + ". " + cleaned
	}
	meta, err := json.Marshal(caption{ID: stamp, SessionID: session, Project: project, Text: cleaned})
	if err != nil {
		return
	}

	q := filepath.Join(home, "queue")
	// Metadata first; the .txt rename is the queue commit marker (matches bash).
	if !writeAtomic(filepath.Join(q, stamp+".caption.json"), meta) {
		return
	}
	if !writeAtomic(filepath.Join(q, stamp+".txt"), []byte(spoken)) {
		os.Remove(filepath.Join(q, stamp+".caption.json"))
		return
	}
	rlog.Log(home, "hook", "queued", map[string]any{
		"id": stamp, "session": session, "project": project, "chars": len(cleaned),
	})
}

func dashDefault(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

func speakAll(home string) bool {
	_, err := os.Stat(filepath.Join(home, "speak-all"))
	return err == nil
}

// projectName mirrors bash: basename(cwd), empty for "-" / missing.
func projectName(cwd string) string {
	if cwd == "-" || cwd == "" {
		return ""
	}
	base := filepath.Base(cwd)
	if base == "-" || base == "." || base == "/" {
		return ""
	}
	return base
}

func writeAtomic(path string, data []byte) bool {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tmp.*")
	if err != nil {
		return false
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return false
	}
	tmp.Sync()
	tmp.Close()
	if os.Rename(tmpName, path) != nil {
		os.Remove(tmpName)
		return false
	}
	return true
}
