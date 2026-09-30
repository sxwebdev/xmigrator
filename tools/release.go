//go:build ignore

// Release preparation runs explicitly with go run tools/release.go.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

const modulePrefix = "github.com/sxwebdev/xmigrator"

var (
	releaseModules = []string{".", "driver/pgx", "driver/sqlite", "cli/urfavecli", "cmd/xmigrator"}
	stableVersion  = regexp.MustCompile(`^v[01]\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)$`)
	releaseHeading = regexp.MustCompile(`(?m)^## (v[^ ]+) — ([0-9]{4}-[0-9]{2}-[0-9]{2})$`)
)

func main() {
	version := flag.String("version", "", "stable release version; defaults to the latest changelog entry")
	notes := flag.String("notes", "", "write release notes to this file")
	tags := flag.Bool("push-tags", false, "create and atomically push module tags to origin")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "release preparation accepts only flags")
		os.Exit(1)
	}
	ctx := context.Background()
	selected, body, err := prepareRelease(ctx, ".", *version)
	if err == nil && *notes != "" {
		err = os.WriteFile(*notes, []byte(body), 0o644)
	}
	if err == nil && *tags {
		err = pushReleaseTags(ctx, ".", selected)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println(selected)
}

func prepareRelease(ctx context.Context, dir, requested string) (string, string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "CHANGELOG.md"))
	if err != nil {
		return "", "", err
	}
	version, notes, err := parseReleaseNotes(string(data), requested)
	if err != nil {
		return "", "", err
	}
	for _, module := range releaseModules {
		path := filepath.Join(dir, module)
		data, err := command(ctx, path, "go", "mod", "edit", "-json")
		if err != nil {
			return "", "", err
		}
		var manifest struct {
			Module  struct{ Path string }
			Require []struct{ Path, Version string }
			Replace []json.RawMessage
		}
		if err := json.Unmarshal([]byte(data), &manifest); err != nil {
			return "", "", err
		}
		name := modulePrefix
		if module != "." {
			name += "/" + module
		}
		if manifest.Module.Path != name || len(manifest.Replace) != 0 {
			return "", "", fmt.Errorf("invalid published manifest: %s", module)
		}
		for _, dep := range manifest.Require {
			if dep.Path == modulePrefix || strings.HasPrefix(dep.Path, modulePrefix+"/") {
				if dep.Version != version {
					return "", "", fmt.Errorf("%s requires %s %s; release requires %s", module, dep.Path, dep.Version, version)
				}
			}
		}
		if _, err := os.Stat(filepath.Join(path, "LICENSE")); err != nil {
			return "", "", err
		}
	}
	return version, notes, nil
}

func parseReleaseNotes(changelog, requested string) (string, string, error) {
	match := releaseHeading.FindStringSubmatchIndex(changelog)
	if match == nil {
		return "", "", errors.New("changelog needs a version and date heading")
	}
	version, date := changelog[match[2]:match[3]], changelog[match[4]:match[5]]
	if !stableVersion.MatchString(version) || requested != "" && requested != version {
		return "", "", errors.New("version must match the latest changelog release and use stable v0.x.y or v1.x.y")
	}
	if _, err := time.Parse(time.DateOnly, date); err != nil {
		return "", "", err
	}
	body := changelog[match[1]:]
	if next := strings.Index(body, "\n## "); next >= 0 {
		body = body[:next]
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return "", "", errors.New("release notes are empty")
	}
	return version, body + "\n", nil
}

func moduleTags(version string) []string {
	tags := []string{version}
	for _, module := range releaseModules[1:] {
		tags = append(tags, module+"/"+version)
	}
	return tags
}

func pushReleaseTags(ctx context.Context, dir, version string) error {
	if !stableVersion.MatchString(version) {
		return errors.New("invalid release version")
	}
	status, err := command(ctx, dir, "git", "status", "--porcelain")
	if err != nil {
		return err
	}
	if status != "" {
		return errors.New("release tags require a clean working tree")
	}
	head, err := command(ctx, dir, "git", "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	remote, err := command(ctx, dir, "git", "ls-remote", "--tags", "origin")
	if err != nil {
		return err
	}
	refs := map[string]string{}
	for _, line := range strings.Split(remote, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 {
			refs[fields[1]] = fields[0]
		}
	}
	tags := moduleTags(version)
	for _, tag := range tags {
		ref := "refs/tags/" + tag
		sha := refs[ref]
		if peeled := refs[ref+"^{}"]; peeled != "" {
			sha = peeled
		}
		if sha != "" && sha != head {
			return fmt.Errorf("remote tag %s points to another commit", tag)
		}
		_, err := command(ctx, dir, "git", "show-ref", "--verify", "--quiet", ref)
		if err == nil {
			sha, err := command(ctx, dir, "git", "rev-parse", ref+"^{commit}")
			if err != nil {
				return err
			}
			if sha != head {
				return fmt.Errorf("local tag %s points to another commit", tag)
			}
		} else {
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 1 {
				return err
			}
		}
	}
	// Check all conflicts before creating any tag. Existing remote tags are reused on retry.
	for _, tag := range tags {
		ref := "refs/tags/" + tag
		if refs[ref] != "" {
			if _, err := command(ctx, dir, "git", "fetch", "origin", ref+":"+ref); err != nil {
				return err
			}
		} else if _, err := command(ctx, dir, "git", "tag", tag, head); err != nil {
			// A matching local tag may remain after an interrupted push.
			sha, inspectErr := command(ctx, dir, "git", "rev-parse", ref+"^{commit}")
			if inspectErr != nil || sha != head {
				return err
			}
		}
	}
	args := []string{"push", "--atomic", "origin"}
	for _, tag := range tags {
		ref := "refs/tags/" + tag
		args = append(args, ref+":"+ref)
	}
	_, err = command(ctx, dir, "git", args...)
	return err
}

func command(ctx context.Context, dir, name string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("%s %v: %w: %s", name, args, err, output)
	}
	return strings.TrimSpace(string(output)), nil
}
