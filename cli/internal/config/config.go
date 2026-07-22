// Package config parses the shell config.sh shared by Raven's components. Env
// vars override, matching bash ${VAR:-default}.
package config

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Config struct {
	MaxSpokenChars  int     // 0 = unlimited
	ChannelTTLHours float64 // idle-channel expiry backstop
	IdleFloor       string  // noise (proven default) or silence
	LiveNarration   bool    // tail speaks completed blocks mid-turn; default off (Phase B gate)
}

func Load(home string) Config {
	cfg := Config{MaxSpokenChars: 0, ChannelTTLHours: 6, IdleFloor: "noise"}
	vals := parseShell(filepath.Join(home, "config.sh"))

	if v := pick("LIVE_NARRATION", vals); v == "1" || strings.EqualFold(v, "true") {
		cfg.LiveNarration = true
	}

	if v := pick("MAX_SPOKEN_CHARS", vals); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			cfg.MaxSpokenChars = n
		}
	}
	if v := pick("CHANNEL_TTL_HOURS", vals); v != "" {
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			cfg.ChannelTTLHours = f
		}
	}
	if v := pick("IDLE_FLOOR", vals); v != "" {
		cfg.IdleFloor = v
	}
	return cfg
}

// pick prefers an explicit env var (bash ${VAR:-...} semantics) then config.sh.
func pick(key string, vals map[string]string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return vals[key]
}

func parseShell(path string) map[string]string {
	out := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		return out
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		val := strings.TrimSpace(line[eq+1:])
		// Strip a trailing "  # comment" (whitespace then #), as bash does.
		if i := strings.Index(val, " #"); i >= 0 {
			val = strings.TrimSpace(val[:i])
		}
		if i := strings.Index(val, "\t#"); i >= 0 {
			val = strings.TrimSpace(val[:i])
		}
		val = strings.Trim(val, `"'`)
		out[key] = val
	}
	return out
}
