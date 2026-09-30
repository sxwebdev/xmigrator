//go:build ignore

package main

import (
	"os"
	"strings"
	"testing"
)

func TestWorkspaceReleaseVersion(t *testing.T) {
	for _, newline := range []string{"\n", "\r\n"} {
		name := "lf"
		if newline == "\r\n" {
			name = "crlf"
		}
		t.Run(name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			text := strings.ReplaceAll("# Changelog\n\n## v0.2.0 — 2026-10-01\n\n- Release\n", "\n", newline)
			if err := os.WriteFile("CHANGELOG.md", []byte(text), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := workspace(); err != nil {
				t.Fatal(err)
			}
			data, err := os.ReadFile("go.work")
			if err != nil {
				t.Fatal(err)
			}
			if strings.Count(string(data), " v0.2.0 => ") != 4 || strings.Contains(string(data), "v0.1.0") {
				t.Fatalf("workspace did not use the changelog version: %s", data)
			}
		})
	}
}

func TestInvalidWorkspaceReleaseVersion(t *testing.T) {
	for _, tt := range []struct{ name, text string }{
		{"missing", ""},
		{"unreleased", "## Unreleased\n"},
		{"unsupported_latest", "## v2.0.0 — 2026-10-01\n\n- Unsupported\n\n## v0.1.0 — 2026-09-01\n\n- Old\n"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Chdir(t.TempDir())
			if tt.text != "" {
				if err := os.WriteFile("CHANGELOG.md", []byte(tt.text), 0o644); err != nil {
					t.Fatal(err)
				}
			}
			if err := workspace(); err == nil {
				t.Fatal("invalid changelog accepted")
			}
			if _, err := os.Stat("go.work"); !os.IsNotExist(err) {
				t.Fatalf("workspace created for an invalid release: %v", err)
			}
		})
	}
}
