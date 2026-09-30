//go:build ignore

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReleaseNotes(t *testing.T) {
	for _, tt := range []struct {
		name, changelog, requested, version, notes string
	}{
		{"stable", "# Changelog\n\n## v0.1.0 — 2026-10-01\n\n- Initial release\n\n## v0.0.1 — 2026-09-01\n\n- Old", "v0.1.0", "v0.1.0", "- Initial release\n"},
		{"inferred", "## v1.2.3 — 2026-10-01\n\n- Stable", "", "v1.2.3", "- Stable\n"},
		{"different_version", "## v0.1.0 — 2026-10-01\n\n- Stable", "v0.2.0", "", ""},
		{"invalid_date", "## v0.1.0 — 2026-02-30\n\n- Stable", "", "", ""},
		{"empty", "## v0.1.0 — 2026-10-01\n\n", "", "", ""},
		{"missing_heading", "# Changelog\n\n## Unreleased\n\n- Stable", "", "", ""},
		{"prerelease", "## v0.1.0-rc.1 — 2026-10-01\n\n- Candidate", "", "", ""},
		{"leading_zero", "## v0.01.0 — 2026-10-01\n\n- Stable", "", "", ""},
		{"major_two", "## v2.0.0 — 2026-10-01\n\n- Stable", "", "", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			version, notes, err := parseReleaseNotes(tt.changelog, tt.requested)
			if tt.version == "" {
				if err == nil {
					t.Fatal("invalid release accepted")
				}
				return
			}
			if err != nil || version != tt.version || notes != tt.notes {
				t.Fatalf("version=%q notes=%q err=%v", version, notes, err)
			}
		})
	}
}

func releaseFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	writeFixture(t, dir, "CHANGELOG.md", "# Changelog\n\n## v0.1.0 — 2026-10-01\n\n- Initial release\n")
	for _, module := range releaseModules {
		name := modulePrefix
		if module != "." {
			name += "/" + module
		}
		manifest := "module " + name + "\n\ngo 1.27.0\n"
		if module != "." {
			manifest += "\nrequire " + modulePrefix + " v0.1.0\n"
		}
		writeFixture(t, dir, filepath.Join(module, "go.mod"), manifest)
		writeFixture(t, dir, filepath.Join(module, "LICENSE"), "MIT\n")
	}
	return dir
}

func writeFixture(t *testing.T, dir, name, contents string) {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPrepareRelease(t *testing.T) {
	for _, change := range []string{"valid", "manifest_path", "dependency", "replace", "license", "missing_changelog", "invalid_manifest"} {
		t.Run(change, func(t *testing.T) {
			dir := releaseFixture(t)
			manifest := "module " + modulePrefix + "/driver/pgx\n\ngo 1.27.0\n\nrequire " + modulePrefix + " v0.1.0\n"
			switch change {
			case "manifest_path":
				manifest = strings.Replace(manifest, "/driver/pgx", "/wrong", 1)
			case "dependency":
				manifest = strings.Replace(manifest, "v0.1.0", "v0.0.1", 1)
			case "replace":
				manifest += "\nreplace " + modulePrefix + " => ../..\n"
			case "license":
				if err := os.Remove(filepath.Join(dir, "driver/pgx/LICENSE")); err != nil {
					t.Fatal(err)
				}
			case "missing_changelog":
				if err := os.Remove(filepath.Join(dir, "CHANGELOG.md")); err != nil {
					t.Fatal(err)
				}
			case "invalid_manifest":
				manifest = "invalid go.mod"
			}
			writeFixture(t, dir, "driver/pgx/go.mod", manifest)
			version, notes, err := prepareRelease(t.Context(), dir, "v0.1.0")
			if change == "valid" {
				if err != nil || version != "v0.1.0" || notes != "- Initial release\n" {
					t.Fatalf("%q %q %v", version, notes, err)
				}
			} else if err == nil {
				t.Fatal("invalid release accepted")
			}
		})
	}
}

func TestReleaseTags(t *testing.T) {
	for _, scenario := range []string{"publish_retry", "local_conflict", "remote_conflict", "dirty", "invalid_version", "atomic_rejection"} {
		t.Run(scenario, func(t *testing.T) {
			dir, remote := t.TempDir(), t.TempDir()
			run := func(where, name string, args ...string) string {
				t.Helper()
				out, err := command(t.Context(), where, name, args...)
				if err != nil {
					t.Fatal(err)
				}
				return out
			}
			run(remote, "git", "init", "--bare")
			run(dir, "git", "init")
			run(dir, "git", "config", "user.email", "tests@example.com")
			run(dir, "git", "config", "user.name", "Release Test")
			run(dir, "git", "commit", "--allow-empty", "-m", "initial")
			run(dir, "git", "remote", "add", "origin", remote)
			version := "v0.1.0"
			switch scenario {
			case "local_conflict", "remote_conflict":
				run(dir, "git", "tag", "driver/pgx/"+version)
				if scenario == "remote_conflict" {
					run(dir, "git", "push", "origin", "refs/tags/driver/pgx/"+version)
					run(dir, "git", "tag", "-d", "driver/pgx/"+version)
				}
				run(dir, "git", "commit", "--allow-empty", "-m", "next")
			case "dirty":
				writeFixture(t, dir, "untracked", "pending")
			case "atomic_rejection":
				writeFixture(t, remote, "hooks/update", "#!/bin/sh\n[ \"$1\" != \"refs/tags/driver/pgx/v0.1.0\" ]\n")
				if err := os.Chmod(filepath.Join(remote, "hooks/update"), 0o755); err != nil {
					t.Fatal(err)
				}
			case "invalid_version":
				version = "v0.1.0; echo bad"
			}
			err := pushReleaseTags(t.Context(), dir, version)
			if scenario != "publish_retry" {
				if err == nil {
					t.Fatal("invalid publication accepted")
				}
				if scenario == "atomic_rejection" {
					refs := run(remote, "git", "for-each-ref", "--format=%(refname)", "refs/tags/")
					if refs != "" {
						t.Fatalf("partial publication after rejected atomic push: %s", refs)
					}
					return
				}
				if tag := run(dir, "git", "tag", "--list", "v0.1.0"); tag != "" {
					t.Fatal("tags created before preflight completed")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := pushReleaseTags(t.Context(), dir, version); err != nil {
				t.Fatalf("retry: %v", err)
			}
			head := run(dir, "git", "rev-parse", "HEAD")
			refs := strings.Fields(run(remote, "git", "show-ref", "--tags"))
			if len(refs) != 10 {
				t.Fatalf("published refs: %v", refs)
			}
			for _, tag := range moduleTags(version) {
				if sha := run(remote, "git", "rev-parse", fmt.Sprintf("refs/tags/%s^{commit}", tag)); sha != head {
					t.Fatalf("tag %s points to %s instead of %s", tag, sha, head)
				}
			}
		})
	}
}
