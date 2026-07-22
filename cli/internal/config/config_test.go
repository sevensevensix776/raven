package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestIdleFloorConfigAndEnvironment(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "config.sh"), []byte("IDLE_FLOOR=silence\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := Load(home).IdleFloor; got != "silence" {
		t.Fatalf("config IDLE_FLOOR: got %q want silence", got)
	}
	t.Setenv("IDLE_FLOOR", "noise")
	if got := Load(home).IdleFloor; got != "noise" {
		t.Fatalf("environment must override config: got %q want noise", got)
	}
}

func TestIdleFloorDefaultsToNoise(t *testing.T) {
	t.Setenv("IDLE_FLOOR", "")
	if got := Load(t.TempDir()).IdleFloor; got != "noise" {
		t.Fatalf("default IDLE_FLOOR: got %q want noise", got)
	}
}
