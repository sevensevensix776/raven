// Package diagnose reports Raven's live process, stream, queue, and event-log
// health. Its metric definitions and presentation mirror ~/speech/diagnose.py.
package diagnose

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"raven-go/internal/hook"
	"raven-go/internal/state"
)

const (
	green  = "\033[32m"
	yellow = "\033[33m"
	red    = "\033[31m"
	dim    = "\033[2m"
	reset  = "\033[0m"
)

// BackendCount preserves Counter's first-seen ordering from diagnose.py.
type BackendCount struct {
	Backend string
	Count   int
}

// Failure is a kokoro_fail or say_fail record in the selected window.
type Failure struct {
	Event string
	ID    any
	Err   any
}

// Metrics contains the event-log values printed by the diagnosis report.
type Metrics struct {
	RepliesSpoken int
	Backends      []BackendCount
	MedianMS      *float64
	MaxMS         *float64
	GateSkips     int
	Fallbacks     int
	Failures      []Failure
}

// Run parses diagnose flags and prints a report for the configured Raven home.
func Run(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("diagnose", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	sinceMin := fs.Int("since-min", 60, "event-log window in minutes")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("diagnose: unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	report(hook.Home(), *sinceMin, out, time.Now())
	return nil
}

func report(home string, sinceMin int, out io.Writer, now time.Time) {
	cutoff := float64(now.UnixNano())/1e9 - float64(sinceMin)*60

	fmt.Fprintf(out, "\n  RAVEN DIAGNOSIS  %s(last %dm)%s\n  %s", dim, sinceMin, reset, strings.Repeat("-", 40))

	fmt.Fprint(out, "\n\n  PROCESSES\n")
	allUp := true
	for _, role := range []string{"writer", "ffmpeg", "server", "synthd"} {
		pid, up := alive(filepath.Join(home, "."+role+".pid"))
		allUp = allUp && up
		fmt.Fprintf(out, "    %16s  %s", mark(up), role)
		if pid != 0 {
			fmt.Fprintf(out, "  %spid %d%s", dim, pid, reset)
		}
		fmt.Fprintln(out)
	}

	var heartbeatAge *float64
	if info, err := os.Stat(filepath.Join(home, "hls", ".heartbeat")); err == nil {
		age := now.Sub(info.ModTime()).Seconds()
		heartbeatAge = &age
	}
	live := heartbeatAge != nil && *heartbeatAge <= 10
	fmt.Fprint(out, "\n  STREAM\n")
	fmt.Fprintf(out, "    listener (phone) polling:  %s  %s(%s)%s\n", yesNo(live), dim, hms(heartbeatAge), reset)
	fmt.Fprintf(out, "    queue pending:             txt=%d wav=%d aiff=%d\n",
		countGlob(filepath.Join(home, "queue", "*.txt")),
		countGlob(filepath.Join(home, "queue", "*.wav")),
		countGlob(filepath.Join(home, "queue", "*.aiff")),
	)
	if selection, ok := readSelection(home); ok {
		fmt.Fprintf(out, "    channel:                   %s -> %s\n", selection.Mode, pointerString(selection.SessionID))
	} else {
		fmt.Fprintln(out, "    channel:                   (none selected)")
	}

	metrics := loadMetrics(filepath.Join(home, "logs", "events.jsonl"), cutoff)
	fmt.Fprint(out, "\n  METRICS\n")
	fmt.Fprintf(out, "    replies spoken:            %d\n", metrics.RepliesSpoken)
	fmt.Fprint(out, "    synth backends:            ")
	if len(metrics.Backends) == 0 {
		fmt.Fprintln(out, "none")
	} else {
		parts := make([]string, 0, len(metrics.Backends))
		for _, backend := range metrics.Backends {
			parts = append(parts, fmt.Sprintf("%s=%d", backend.Backend, backend.Count))
		}
		fmt.Fprintln(out, strings.Join(parts, ", "))
	}
	if metrics.MedianMS != nil {
		fmt.Fprintf(out, "    synth latency:             median %.0fms  max %.0fms\n", *metrics.MedianMS, *metrics.MaxMS)
	}
	fmt.Fprintf(out, "    gate skips (other chans):  %d\n", metrics.GateSkips)
	fallbackColor := dim
	if metrics.Fallbacks != 0 {
		fallbackColor = yellow
	}
	fmt.Fprintf(out, "    %skokoro->say fallbacks:      %d%s\n", fallbackColor, metrics.Fallbacks, reset)

	phoneCount, lastPhone, phoneOK := loadPhone(filepath.Join(home, "logs", "phone.jsonl"))
	if phoneOK {
		fmt.Fprint(out, "\n  PHONE\n")
		fmt.Fprintf(out, "    log lines uploaded:        %d\n", phoneCount)
		fmt.Fprintf(out, "    last:                      %s%s%s\n", dim, truncateRunes(lastPhone, 60), reset)
	} else {
		fmt.Fprint(out, "\n  PHONE\n    no uploads yet (app hasn't posted /log)\n")
	}

	fmt.Fprint(out, "\n  ERRORS\n")
	if len(metrics.Failures) == 0 {
		fmt.Fprintf(out, "    %snone%s\n", green, reset)
	} else {
		start := len(metrics.Failures) - 3
		if start < 0 {
			start = 0
		}
		for _, failure := range metrics.Failures[start:] {
			fmt.Fprintf(out, "    %s%s%s  id=%s  %s%s%s\n", red, failure.Event, reset,
				pythonString(failure.ID), dim, truncateRunes(pythonString(failure.Err), 70), reset)
		}
	}

	fmt.Fprintf(out, "\n  %s\n", strings.Repeat("-", 40))
	healthy := allUp && len(metrics.Failures) == 0
	verdict := red + "NEEDS ATTENTION" + reset
	if healthy {
		verdict = green + "HEALTHY" + reset
	}
	fmt.Fprintf(out, "  VERDICT: %s\n\n", verdict)
}

// Aggregate reads JSONL event records at or after cutoff and applies the exact
// counters used by diagnose.py. Malformed and non-object lines are skipped.
func Aggregate(r io.Reader, cutoff float64) Metrics {
	var metrics Metrics
	backendIndex := map[string]int{}
	var latencies []float64

	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		record, ok := decodeRecord(scanner.Text())
		if !ok {
			continue
		}
		ts, ok := number(record["ts"])
		if !ok || ts < cutoff {
			continue
		}

		event, _ := record["event"].(string)
		switch event {
		case "queued":
			metrics.RepliesSpoken++
		case "gate_skip":
			metrics.GateSkips++
		case "kokoro_fail", "say_fail":
			metrics.Failures = append(metrics.Failures, Failure{Event: event, ID: record["id"], Err: record["err"]})
		case "synth":
			if !truthy(record["ok"]) {
				continue
			}
			backend := pythonString(record["backend"])
			if i, exists := backendIndex[backend]; exists {
				metrics.Backends[i].Count++
			} else {
				backendIndex[backend] = len(metrics.Backends)
				metrics.Backends = append(metrics.Backends, BackendCount{Backend: backend, Count: 1})
			}
			if backend == "say" {
				metrics.Fallbacks++
			}
			if ms, ok := number(record["ms"]); ok {
				latencies = append(latencies, ms)
			}
		}
	}

	if len(latencies) != 0 {
		sort.Float64s(latencies)
		median := latencies[len(latencies)/2]
		max := latencies[len(latencies)-1]
		metrics.MedianMS = &median
		metrics.MaxMS = &max
	}
	return metrics
}

func loadMetrics(path string, cutoff float64) Metrics {
	f, err := os.Open(path)
	if err != nil {
		return Metrics{}
	}
	defer f.Close()
	return Aggregate(f, cutoff)
}

func loadPhone(path string) (count int, last string, ok bool) {
	f, err := os.Open(path)
	if err != nil {
		return 0, "", false
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var record map[string]any
		if json.Unmarshal(scanner.Bytes(), &record) != nil || record == nil {
			continue
		}
		count++
		line, _ := record["line"].(string)
		last = line
	}
	return count, last, true
}

func readSelection(home string) (state.Selection, bool) {
	data, err := os.ReadFile(filepath.Join(home, "selection.json"))
	if err != nil || !json.Valid(data) {
		return state.Selection{}, false
	}
	var object map[string]any
	if json.Unmarshal(data, &object) != nil || object == nil {
		return state.Selection{}, false
	}
	return state.ReadSelection(home), true
}

func alive(path string) (int, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || syscall.Kill(pid, 0) != nil {
		return 0, false
	}
	return pid, true
}

func decodeRecord(line string) (map[string]any, bool) {
	var record map[string]any
	decoder := json.NewDecoder(strings.NewReader(line))
	decoder.UseNumber()
	if decoder.Decode(&record) != nil || record == nil {
		return nil, false
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, false
	}
	return record, true
}

func countGlob(pattern string) int {
	matches, _ := filepath.Glob(pattern)
	return len(matches)
}

func hms(age *float64) string {
	if age == nil {
		return "never"
	}
	if *age < 90 {
		return fmt.Sprintf("%.0fs ago", *age)
	}
	if *age < 5400 {
		return fmt.Sprintf("%.0fm ago", *age/60)
	}
	return fmt.Sprintf("%.1fh ago", *age/3600)
}

func mark(good bool) string {
	if good {
		return green + "OK" + reset
	}
	return red + "FAIL" + reset
}

func yesNo(value bool) string {
	if value {
		return "yes"
	}
	return "no"
}

func pointerString(value *string) string {
	if value == nil {
		return "None"
	}
	return *value
}

func number(value any) (float64, bool) {
	switch value := value.(type) {
	case json.Number:
		n, err := value.Float64()
		return n, err == nil
	case float64:
		return value, true
	case float32:
		return float64(value), true
	case int:
		return float64(value), true
	case int64:
		return float64(value), true
	case bool: // bool is a numeric subtype in Python.
		if value {
			return 1, true
		}
		return 0, true
	default:
		return 0, false
	}
}

func truthy(value any) bool {
	switch value := value.(type) {
	case nil:
		return false
	case bool:
		return value
	case string:
		return value != ""
	case json.Number:
		n, err := value.Float64()
		return err == nil && n != 0
	case []any:
		return len(value) != 0
	case map[string]any:
		return len(value) != 0
	default:
		return true
	}
}

func pythonString(value any) string {
	switch value := value.(type) {
	case nil:
		return "None"
	case bool:
		if value {
			return "True"
		}
		return "False"
	case json.Number:
		return value.String()
	case string:
		return value
	default:
		return fmt.Sprint(value)
	}
}

func truncateRunes(value string, limit int) string {
	runes := []rune(value)
	if len(runes) > limit {
		runes = runes[:limit]
	}
	return string(runes)
}
