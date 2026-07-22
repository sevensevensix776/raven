package diagnose

import (
	"strings"
	"testing"
)

func TestAggregateMetricsMatchesPythonDefinitions(t *testing.T) {
	events := strings.Join([]string{
		`{"ts":900,"event":"queued"}`,
		`not json`,
		`{"ts":1000,"comp":"hook","event":"queued"}`,
		`{"ts":1001,"comp":"hook","event":"queued"}`,
		`{"ts":1002,"comp":"synthd","event":"synth","ok":true,"backend":"kokoro","ms":100}`,
		`{"ts":1003,"comp":"synthd","event":"synth","ok":true,"backend":"say","ms":250}`,
		`{"ts":1004,"comp":"synthd","event":"synth","ok":true,"backend":"kokoro","ms":200}`,
		`{"ts":1005,"comp":"synthd","event":"synth","ok":true,"backend":"say","ms":300}`,
		`{"ts":1006,"comp":"synthd","event":"synth","ok":false,"backend":"say","ms":999}`,
		`{"ts":1007,"comp":"synthd","event":"synth","ok":true,"backend":"kokoro","ms":"slow"}`,
		`{"ts":1008,"comp":"hook","event":"gate_skip"}`,
		`{"ts":1009,"comp":"hook","event":"gate_skip"}`,
		`{"ts":1010,"comp":"synthd","event":"kokoro_fail","id":"a","err":"boom"}`,
		`{"ts":1011,"comp":"synthd","event":"say_fail","id":"b","err":"also boom"}`,
	}, "\n")

	got := Aggregate(strings.NewReader(events), 1000)
	if got.RepliesSpoken != 2 {
		t.Errorf("RepliesSpoken = %d, want 2", got.RepliesSpoken)
	}
	if len(got.Backends) != 2 || got.Backends[0] != (BackendCount{Backend: "kokoro", Count: 3}) || got.Backends[1] != (BackendCount{Backend: "say", Count: 2}) {
		t.Errorf("Backends = %#v, want first-seen kokoro=3, say=2", got.Backends)
	}
	if got.MedianMS == nil || *got.MedianMS != 250 {
		t.Errorf("MedianMS = %v, want upper-middle 250", got.MedianMS)
	}
	if got.MaxMS == nil || *got.MaxMS != 300 {
		t.Errorf("MaxMS = %v, want 300", got.MaxMS)
	}
	if got.GateSkips != 2 {
		t.Errorf("GateSkips = %d, want 2", got.GateSkips)
	}
	if got.Fallbacks != 2 {
		t.Errorf("Fallbacks = %d, want 2 successful say synths", got.Fallbacks)
	}
	if len(got.Failures) != 2 || got.Failures[0].Event != "kokoro_fail" || got.Failures[1].Event != "say_fail" {
		t.Errorf("Failures = %#v, want kokoro_fail then say_fail", got.Failures)
	}
}

func TestAggregateEmptyAndMalformedInput(t *testing.T) {
	got := Aggregate(strings.NewReader("\n{bad}\n[]\n{}\n"), 1000)
	if got.RepliesSpoken != 0 || len(got.Backends) != 0 || got.MedianMS != nil || got.MaxMS != nil || got.GateSkips != 0 || got.Fallbacks != 0 || len(got.Failures) != 0 {
		t.Fatalf("malformed input should be skipped, got %#v", got)
	}
}
