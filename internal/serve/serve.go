// Package serve ports Raven's Python HTTP control server and HLS file server.
package serve

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"raven-go/internal/hook"
	"raven-go/internal/rlog"
	"raven-go/internal/state"
)

const defaultAddr = "100.64.0.1:8080"

type handler struct {
	home   string
	static http.Handler
	now    func() time.Time
}

type selectionResponse struct {
	Mode      string  `json:"mode"`
	SessionID *string `json:"session_id"`
}

type channelsResponse struct {
	Channels  []state.Channel   `json:"channels"`
	Selection selectionResponse `json:"selection"`
}

type transcriptResponse struct {
	Lines []json.RawMessage `json:"lines"`
}

type catchupLine struct {
	ID            string  `json:"id"`
	SessionID     string  `json:"session_id"`
	Project       string  `json:"project"`
	Text          string  `json:"text"`
	Role          string  `json:"role"`
	Catchup       bool    `json:"catchup"`
	SpokenAtEpoch float64 `json:"spoken_at_epoch"`
}

type catchupResponse struct {
	Lines []catchupLine `json:"lines"`
}

type queuePending struct {
	Txt  int `json:"txt"`
	Wav  int `json:"wav"`
	Aiff int `json:"aiff"`
}

type healthSelection struct {
	Mode      any `json:"mode"`
	SessionID any `json:"session_id"`
}

type healthResponse struct {
	TS            float64         `json:"ts"`
	HeartbeatAgeS *float64        `json:"heartbeat_age_s"`
	ListenerLive  bool            `json:"listener_live"`
	QueuePending  queuePending    `json:"queue_pending"`
	Selection     healthSelection `json:"selection"`
	Channels      int             `json:"channels"`
	LastSpoken    map[string]any  `json:"last_spoken"`
}

type activeRequest struct {
	Mode      string  `json:"mode"`
	SessionID *string `json:"session_id"`
}

type logRecord struct {
	Device any    `json:"device"`
	Line   string `json:"line"`
}

// Run parses serve flags and blocks serving HTTP until the server exits.
func Run(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	addrDefault := os.Getenv("RAVEN_BIND")
	if addrDefault == "" {
		addrDefault = defaultAddr
	}
	addr := fs.String("addr", addrDefault, "listen address (host:port)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return fmt.Errorf("serve: unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	listenAddr, err := normalizeAddr(*addr)
	if err != nil {
		return err
	}
	server := &http.Server{
		Addr:              listenAddr,
		Handler:           NewHandler(hook.Home()),
		ReadHeaderTimeout: 10 * time.Second,
	}
	if inherited := os.Getenv("RAVEN_SERVE_FDS"); inherited != "" {
		listener, err := inheritedListener(inherited)
		if err != nil {
			return err
		}
		return server.Serve(listener)
	}
	return server.ListenAndServe()
}

type connListener struct {
	connections []net.Conn
	next        int
	wait        chan struct{}
}

func inheritedListener(raw string) (net.Listener, error) {
	listener := &connListener{wait: make(chan struct{})}
	for _, value := range strings.Split(raw, ",") {
		fd, err := strconv.Atoi(value)
		if err != nil || fd < 0 {
			return nil, fmt.Errorf("serve: invalid inherited fd %q", value)
		}
		file := os.NewFile(uintptr(fd), "raven-serve-parity")
		connection, err := net.FileConn(file)
		_ = file.Close()
		if err != nil {
			return nil, fmt.Errorf("serve: inherited fd %d: %w", fd, err)
		}
		listener.connections = append(listener.connections, connection)
	}
	return listener, nil
}

func (l *connListener) Accept() (net.Conn, error) {
	if l.next < len(l.connections) {
		connection := l.connections[l.next]
		l.next++
		return connection, nil
	}
	<-l.wait
	return nil, net.ErrClosed
}

func (l *connListener) Close() error {
	select {
	case <-l.wait:
	default:
		close(l.wait)
	}
	for _, connection := range l.connections[l.next:] {
		_ = connection.Close()
	}
	return nil
}

func (l *connListener) Addr() net.Addr {
	return localAddr("raven-serve-parity")
}

type localAddr string

func (a localAddr) Network() string { return "local" }
func (a localAddr) String() string  { return string(a) }

func normalizeAddr(addr string) (string, error) {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "", errors.New("serve: listen address cannot be empty")
	}
	if _, _, err := net.SplitHostPort(addr); err == nil {
		return addr, nil
	}
	if ip := net.ParseIP(addr); ip != nil {
		return net.JoinHostPort(addr, "8080"), nil
	}
	if !strings.Contains(addr, ":") {
		return net.JoinHostPort(addr, "8080"), nil
	}
	return "", fmt.Errorf("serve: invalid listen address %q", addr)
}

// NewHandler returns the complete JSON API and HLS handler for a Raven home.
func NewHandler(home string) http.Handler {
	return &handler{
		home:   home,
		static: http.FileServer(http.Dir(filepath.Join(home, "hls"))),
		now:    time.Now,
	}
}

func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		switch r.URL.Path {
		case "/health":
			h.jsonResponse(w, r, healthSnapshot(h.home, h.now()), http.StatusOK, false)
			return
		case "/channels":
			h.handleChannels(w, r)
			return
		case "/transcript":
			h.handleTranscript(w, r)
			return
		case "/catchup":
			h.handleCatchup(w, r)
			return
		}
	}
	if r.Method == http.MethodPost {
		switch r.URL.Path {
		case "/active":
			h.handleActive(w, r)
			return
		case "/log":
			h.handleLog(w, r)
			return
		default:
			http.NotFound(w, r)
			return
		}
	}

	if strings.HasSuffix(r.URL.Path, ".m3u8") {
		w.Header().Set("Content-Type", "application/vnd.apple.mpegurl")
		w.Header().Set("Cache-Control", "no-store")
		if r.Method == http.MethodGet {
			touch(filepath.Join(h.home, "hls", ".heartbeat"), h.now())
		}
	} else if strings.HasSuffix(r.URL.Path, ".ts") {
		w.Header().Set("Content-Type", "video/mp2t")
	}
	h.static.ServeHTTP(w, r)
}

func (h *handler) handleChannels(w http.ResponseWriter, r *http.Request) {
	unlock, err := state.Lock(h.home)
	if err != nil {
		http.Error(w, "state unavailable", http.StatusInternalServerError)
		return
	}
	channels := state.ReadChannels(h.home)
	selection := state.ReadSelection(h.home)
	unlock()

	sort.SliceStable(channels, func(i, j int) bool {
		return channels[i].LastActiveEpoch > channels[j].LastActiveEpoch
	})
	h.jsonResponse(w, r, channelsResponse{
		Channels: channels,
		Selection: selectionResponse{
			Mode: selection.Mode, SessionID: selection.SessionID,
		},
	}, http.StatusOK, true)
}

func (h *handler) handleTranscript(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		if parsed, err := strconv.Atoi(raw); err == nil {
			limit = parsed
		}
	}
	if limit < 1 {
		limit = 1
	}
	if limit > 100 {
		limit = 100
	}
	h.jsonResponse(w, r, transcriptResponse{Lines: transcriptLines(h.home, limit)}, http.StatusOK, true)
}

func (h *handler) handleCatchup(w http.ResponseWriter, r *http.Request) {
	session := r.URL.Query().Get("session")
	unlock, err := state.Lock(h.home)
	if err != nil {
		http.Error(w, "state unavailable", http.StatusInternalServerError)
		return
	}
	channels := state.ReadChannels(h.home)
	unlock()

	lines := []catchupLine{}
	for _, channel := range channels {
		if channel.SessionID != session {
			continue
		}
		for i, recent := range channel.Recent {
			lines = append(lines, catchupLine{
				ID:        fmt.Sprintf("catchup-%s-%d-%d", prefixRunes(session, 8), i, int64(recent.At)),
				SessionID: session, Project: channel.Project, Text: recent.Text,
				Role: "claude", Catchup: true, SpokenAtEpoch: recent.At,
			})
		}
		break
	}
	h.jsonResponse(w, r, catchupResponse{Lines: lines}, http.StatusOK, false)
}

func (h *handler) handleActive(w http.ResponseWriter, r *http.Request) {
	length, ok := requestLength(w, r, 4096, "Body must be 1..4096 bytes")
	if !ok {
		return
	}
	var requested activeRequest
	if err := decodeBody(r.Body, length, &requested); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	if requested.Mode != "follow" && requested.Mode != "pinned" {
		http.Error(w, "mode must be follow or pinned", http.StatusBadRequest)
		return
	}

	unlock, err := state.Lock(h.home)
	if err != nil {
		http.Error(w, "state unavailable", http.StatusInternalServerError)
		return
	}
	defer unlock()
	selection := state.ReadSelection(h.home)
	if requested.Mode == "pinned" {
		known := false
		if requested.SessionID != nil {
			for _, channel := range state.ReadChannels(h.home) {
				if channel.SessionID == *requested.SessionID {
					known = true
					break
				}
			}
		}
		if !known {
			http.Error(w, "Unknown session_id", http.StatusBadRequest)
			return
		}
		selection.Mode = "pinned"
		selection.SessionID = requested.SessionID
	} else {
		selection.Mode = "follow"
		selection.SessionID = selection.FollowSessionID
	}
	if err := state.WriteJSON(filepath.Join(h.home, "selection.json"), selection); err != nil {
		http.Error(w, "state unavailable", http.StatusInternalServerError)
		return
	}
	h.jsonResponse(w, r, selectionResponse{
		Mode: selection.Mode, SessionID: selection.SessionID,
	}, http.StatusOK, false)
}

func (h *handler) handleLog(w http.ResponseWriter, r *http.Request) {
	length, ok := requestLength(w, r, 262144, "Body must be 1..256KiB")
	if !ok {
		return
	}
	var body map[string]json.RawMessage
	if err := decodeBody(r.Body, length, &body); err != nil || body == nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}
	device := any("iphone")
	if raw, exists := body["device"]; exists {
		if err := json.Unmarshal(raw, &device); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
	}
	lines := []any{}
	if raw, exists := body["lines"]; exists {
		if err := json.Unmarshal(raw, &lines); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}
	}

	var out bytes.Buffer
	for i, line := range lines {
		if i == 2000 {
			break
		}
		encoded, err := marshalCompact(logRecord{Device: device, Line: prefixRunes(pythonString(line), 2000)})
		if err != nil {
			continue
		}
		out.Write(encoded)
		out.WriteByte('\n')
	}
	logDir := filepath.Join(h.home, "logs")
	if err := os.MkdirAll(logDir, 0o755); err == nil && out.Len() > 0 {
		if f, openErr := os.OpenFile(filepath.Join(logDir, "phone.jsonl"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644); openErr == nil {
			_, _ = f.Write(out.Bytes())
			_ = f.Close()
		}
	}
	rlog.Log(h.home, "phone", "log_upload", map[string]any{"device": device, "n": len(lines)})
	h.jsonResponse(w, r, struct {
		Received int `json:"received"`
	}{Received: len(lines)}, http.StatusOK, false)
}

func requestLength(w http.ResponseWriter, r *http.Request, maximum int, message string) (int, bool) {
	raw := r.Header.Get("Content-Length")
	length := 0
	if raw != "" {
		var err error
		length, err = strconv.Atoi(raw)
		if err != nil {
			http.Error(w, "Invalid Content-Length", http.StatusBadRequest)
			return 0, false
		}
	}
	if length <= 0 || length > maximum {
		http.Error(w, message, http.StatusRequestEntityTooLarge)
		return 0, false
	}
	return length, true
}

func decodeBody(r io.Reader, length int, target any) error {
	body, err := io.ReadAll(io.LimitReader(r, int64(length)))
	if err != nil {
		return err
	}
	return json.Unmarshal(body, target)
}

func (h *handler) jsonResponse(w http.ResponseWriter, r *http.Request, payload any, status int, conditional bool) {
	body, err := marshalCompact(payload)
	if err != nil {
		http.Error(w, "json unavailable", http.StatusInternalServerError)
		return
	}
	tag := etag(body)
	if conditional && r.Header.Get("If-None-Match") == tag {
		w.Header().Set("ETag", tag)
		w.Header().Set("Cache-Control", "no-cache")
		w.WriteHeader(http.StatusNotModified)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(body)))
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("ETag", tag)
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func etag(body []byte) string {
	sum := sha256.Sum256(body)
	return fmt.Sprintf("\"%x\"", sum[:10])
}

func marshalCompact(value any) ([]byte, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return nil, err
	}
	encoded := bytes.TrimSuffix(buf.Bytes(), []byte{'\n'})
	return escapeNonASCII(encoded), nil
}

func escapeNonASCII(encoded []byte) []byte {
	if !utf8.Valid(encoded) {
		return encoded
	}
	var out bytes.Buffer
	out.Grow(len(encoded))
	for len(encoded) > 0 {
		r, size := utf8.DecodeRune(encoded)
		encoded = encoded[size:]
		if r < utf8.RuneSelf {
			out.WriteByte(byte(r))
			continue
		}
		if r <= 0xffff {
			fmt.Fprintf(&out, "\\u%04x", r)
			continue
		}
		r -= 0x10000
		fmt.Fprintf(&out, "\\u%04x\\u%04x", 0xd800+(r>>10), 0xdc00+(r&0x3ff))
	}
	return out.Bytes()
}

func transcriptLines(home string, limit int) []json.RawMessage {
	b, err := os.ReadFile(filepath.Join(home, "spoken.jsonl"))
	if err != nil {
		return []json.RawMessage{}
	}
	lines := pythonSplitLines(string(b))
	if len(lines) > limit {
		lines = lines[len(lines)-limit:]
	}
	result := []json.RawMessage{}
	for _, line := range lines {
		raw := json.RawMessage(line)
		if json.Valid(raw) {
			result = append(result, raw)
		}
	}
	return result
}

func healthSnapshot(home string, now time.Time) healthResponse {
	nowSeconds := float64(now.UnixNano()) / 1e9
	var heartbeatAge *float64
	if info, err := os.Stat(filepath.Join(home, "hls", ".heartbeat")); err == nil {
		age := math.RoundToEven((nowSeconds-float64(info.ModTime().UnixNano())/1e9)*10) / 10
		heartbeatAge = &age
	}
	pending := queuePending{
		Txt:  globCount(filepath.Join(home, "queue", "*.txt")),
		Wav:  globCount(filepath.Join(home, "queue", "*.wav")),
		Aiff: globCount(filepath.Join(home, "queue", "*.aiff")),
	}
	mode, sessionID := healthSelectionState(home)
	return healthResponse{
		TS:            math.RoundToEven(nowSeconds*10) / 10,
		HeartbeatAgeS: heartbeatAge,
		ListenerLive:  heartbeatAge != nil && *heartbeatAge <= 10,
		QueuePending:  pending,
		Selection:     healthSelection{Mode: mode, SessionID: sessionID},
		Channels:      len(state.ReadChannels(home)),
		LastSpoken:    lastSpokenLine(home),
	}
}

func healthSelectionState(home string) (any, any) {
	var raw map[string]any
	b, err := os.ReadFile(filepath.Join(home, "selection.json"))
	if err != nil || json.Unmarshal(b, &raw) != nil {
		return nil, nil
	}
	return raw["mode"], raw["session_id"]
}

func lastSpokenLine(home string) map[string]any {
	b, err := os.ReadFile(filepath.Join(home, "spoken.jsonl"))
	if err != nil {
		return nil
	}
	lines := pythonSplitLines(string(b))
	if len(lines) == 0 {
		return nil
	}
	var record map[string]any
	if json.Unmarshal([]byte(lines[len(lines)-1]), &record) != nil {
		return nil
	}
	if text, ok := record["text"].(string); ok {
		record["chars"] = utf8.RuneCountInString(text)
		record["text"] = prefixRunes(text, 120)
	}
	return record
}

func pythonSplitLines(text string) []string {
	if text == "" {
		return nil
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	text = strings.ReplaceAll(text, "\r", "\n")
	if strings.HasSuffix(text, "\n") {
		text = strings.TrimSuffix(text, "\n")
	}
	return strings.Split(text, "\n")
}

func globCount(pattern string) int {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return 0
	}
	return len(matches)
}

func touch(path string, now time.Time) {
	if err := os.Chtimes(path, now, now); err == nil {
		return
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o666)
	if err != nil {
		return
	}
	_ = f.Close()
	_ = os.Chtimes(path, now, now)
}

func prefixRunes(text string, n int) string {
	if utf8.RuneCountInString(text) <= n {
		return text
	}
	return string([]rune(text)[:n])
}

func pythonString(value any) string {
	switch v := value.(type) {
	case nil:
		return "None"
	case string:
		return v
	case bool:
		if v {
			return "True"
		}
		return "False"
	case float64:
		return strconv.FormatFloat(v, 'g', -1, 64)
	default:
		return fmt.Sprint(v)
	}
}
