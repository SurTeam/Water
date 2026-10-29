package goconfig

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestXDGPathPrecedence(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", "")
	alias := t.TempDir()
	t.Setenv("XDG_CONFIG", alias)
	if got := DefaultPath("release"); got != filepath.Join(alias, "water", "config.json") {
		t.Fatal(got)
	}
	primary := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", primary)
	if got := DefaultLoadPath("dev"); got != filepath.Join(primary, "water-dev", "config.json") {
		t.Fatal(got)
	}
	t.Setenv("XDG_CONFIG_HOME", "relative")
	t.Setenv("XDG_CONFIG", "relative")
	expected := filepath.Join(os.Getenv("HOME"), ".config", "water", "config.json")
	if runtime.GOOS == "windows" {
		root, err := os.UserConfigDir()
		if err != nil {
			t.Fatal(err)
		}
		expected = filepath.Join(root, "water", "config.json")
	}
	if got := DefaultPath("release"); got != expected {
		t.Fatal(got)
	}
}

func TestLegacyMigrationPreservesBytesAndExistingTarget(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("macOS legacy path")
	}
	t.Setenv("HOME", t.TempDir())
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("XDG_CONFIG", "")
	for _, variant := range []string{"release", "dev"} {
		source, target := legacyPath(variant), StandardPath(variant)
		original := []byte("{\n \"unknown\": {\"keep\": true}\n}\n")
		if err := os.MkdirAll(filepath.Dir(source), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(source, original, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(target); err != nil {
			t.Fatal(err)
		}
		for _, path := range []string{source, target} {
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, original) {
				t.Fatalf("%s: %s %v", path, got, err)
			}
		}
		info, _ := os.Stat(target)
		if info.Mode().Perm() != 0600 {
			t.Fatal(info.Mode())
		}
		replacement := []byte(`{"keep":"destination"}`)
		if err := os.WriteFile(target, replacement, 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(target); err != nil {
			t.Fatal(err)
		}
		got, _ := os.ReadFile(target)
		if !bytes.Equal(got, replacement) {
			t.Fatal("overwrote existing configuration")
		}
	}
}
