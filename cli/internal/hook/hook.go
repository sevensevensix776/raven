// Package hook implements Raven's Claude Code hook: read a hook payload on
// stdin, maintain the channel registry, and — for the selected channel's Stop,
// when the tailer is not running — queue the reply for synthesis.
package hook

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"raven-go/internal/clean"
	"raven-go/internal/config"
	"raven-go/internal/rctitle"
	"raven-go/internal/rlog"
	"raven-go/internal/state"
	"raven-go/internal/transcript"
)

type payload struct {
	Event          string `json:"hook_event_name"`
	Session        string `json:"session_id"`
	Cwd            string `json:"cwd"`
	Message        string `json:"last_assistant_message"`
	Prompt         string `json:"prompt"`
	TranscriptPath string `json:"transcript_path"`
}

type caption struct {
	ID        string `json:"id"`
	SessionID string `json:"session_id"`
	Project   string `json:"project"`
	Text      string `json:"text"`
	Display   string `json:"display"`
}

// Home resolves the Raven dir: RAVEN_HOME override (for tests, and exported by
// start.sh) else ~/code/experiments/raven. The Claude Code hook invokes this
// binary with no environment, so this default must name the real runtime home.
func Home() string {
	if h := os.Getenv("RAVEN_HOME"); h != "" {
		return h
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "code", "experiments", "raven")
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

	// Don't let a system-injected event (task notification) become the channel
	// preview; the registry keeps the prior line when this is empty.
	registryLine := clean.Collapse(rawText, 280)
	if isSystemInjected(rawText) {
		registryLine = ""
	}
	name := rctitle.Read(p.TranscriptPath)
	state.UpdateRegistry(home, event, session, cwd, registryLine, name, p.TranscriptPath, cfg.ChannelTTLHours)

	switch event {
	case "UserPromptSubmit":
		// The user speaking supersedes everything queued before this moment: they
		// have replied on this thread, so narration of what came earlier describes
		// a conversation that has already moved on. Record a watermark and the
		// tailer drops those clips on its next pass (~300ms). System-injected
		// messages are excluded — a task notification is not the user talking, and
		// must not silence real narration.
		if !isSystemInjected(rawText) {
			if unlock, err := state.Lock(home); err == nil {
				state.SetPromptWatermark(home, session)
				unlock()
			} else {
				state.SetPromptWatermark(home, session)
			}
		}
		// Record the prompt in the transcript (screen only) for the selected
		// channel — the registry above already made it active in follow mode.
		// Skip harness-injected messages (task notifications, system reminders):
		// Claude Code surfaces those as user-role prompts, but they are not the
		// user talking and must not pollute the transcript.
		if !isSystemInjected(rawText) && (speakAll(home) || session == state.SelectedSession(home)) {
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

	// Live narration: when on and the tailer is alive, it enqueues every completed
	// block straight from the transcript — including this final one — so the Stop
	// hook must not also enqueue it or the driver hears the conclusion twice. If
	// the tailer is down, fall through and speak the final block ourselves (the
	// safety net that preserves today's behavior).
	if cfg.LiveNarration && tailerAlive(home) {
		rlog.Log(home, "hook", "stop_yield_to_tailer", map[string]any{
			"session": session, "project": projectName(cwd),
		})
		return
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
	meta, err := json.Marshal(caption{
		ID: stamp, SessionID: session, Project: project, Text: cleaned,
		Display: clean.Display(rawText),
	})
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
		"writer": WriterAlive(home),
	})
	if !WriterAlive(home) {
		// Nothing will ever play this. Say so loudly: this is the one failure the
		// producer can see but the driver cannot, because dead-pipeline silence
		// sounds exactly like Claude having nothing to say.
		rlog.Log(home, "hook", "queued_but_pipeline_down", map[string]any{
			"id": stamp, "session": session, "project": project,
			"hint": "run start.sh (or install the watchdog LaunchAgent)",
		})
	}
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

// tailerAlive reports whether the live-narration tailer is running, via its
// pidfile and a signal-0 liveness probe (same semantics as `kill -0`). Used by
// the Stop hook to decide whether to yield final-block speech to the tailer.
func tailerAlive(home string) bool { return pidAlive(home, ".tail.pid") }

// WriterAlive reports whether `raven write` — the process that drains the queue
// into the audio timeline — is running. Producers use this to detect the failure
// mode that is otherwise completely silent: the hook is spawned fresh by Claude
// Code on every event, so it happily keeps queueing speech even when the whole
// pipeline is dead. A Mac reboot with no writer running once buried 350 unplayed
// clips over a day, and from the driver's side that is indistinguishable from
// Claude simply having nothing to say.
func WriterAlive(home string) bool { return pidAlive(home, ".writer.pid") }

// pidAlive reports whether the pid recorded in home/<pidfile> is a live process.
// Signal 0 performs the permission and existence checks without delivering.
func pidAlive(home, pidfile string) bool {
	b, err := os.ReadFile(filepath.Join(home, pidfile))
	if err != nil {
		return false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// isSystemInjected reports whether a UserPromptSubmit prompt is actually a
// harness-injected message (async task notification, system reminder) rather
// than the user typing. Claude Code delivers these as user-role prompts.
func isSystemInjected(text string) bool {
	t := strings.TrimSpace(text)
	for _, marker := range []string{
		"<task-notification>",
		"[SYSTEM NOTIFICATION - NOT USER INPUT]",
		"<system-reminder>",
		"<command-name>",
		"<local-command-stdout>",
	} {
		if strings.Contains(t, marker) {
			return true
		}
	}
	return false
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
