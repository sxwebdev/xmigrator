//go:build ignore

// Developer checks are run explicitly with go run tools/dev.go <command>.
package main

import (
	"archive/zip"
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
)

const prefix = "github.com/sxwebdev/xmigrator"

var modules = []string{".", "driver/pgx", "driver/sqlite", "cli/urfavecli", "cmd/xmigrator", "internal/integration"}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if err := dispatch(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func dispatch(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return errors.New("usage: go run tools/dev.go workspace|check|verify-install|prove-regressions")
	}
	if _, err := os.Stat("migrator.go"); err != nil {
		return errors.New("run developer tools from the repository root")
	}
	switch args[0] {
	case "workspace":
		if len(args) != 1 {
			return errors.New("workspace accepts no arguments")
		}
		return workspace()
	case "check":
		return check(ctx, args[1:])
	case "verify-install":
		if len(args) != 1 {
			return errors.New("verify-install accepts no arguments")
		}
		return verifyInstall(ctx)
	case "prove-regressions":
		if len(args) != 1 {
			return errors.New("prove-regressions accepts no arguments")
		}
		return proveRegressions(ctx)
	default:
		return fmt.Errorf("unknown developer command %q", args[0])
	}
}

func workspace() error {
	version, err := releaseVersion()
	if err != nil {
		return err
	}
	var b strings.Builder
	b.WriteString("go 1.27.0\n\ntoolchain go1.27.1\n\nuse (\n")
	for _, module := range modules {
		fmt.Fprintf(&b, " ./%s\n", module)
	}
	b.WriteString(")\n\nreplace (\n")
	for _, module := range modules[:4] {
		name := prefix
		if module != "." {
			name += "/" + module
		}
		fmt.Fprintf(&b, " %s %s => ./%s\n", name, version, module)
	}
	b.WriteString(")\n")
	return os.WriteFile("go.work", []byte(b.String()), 0o644)
}

func releaseVersion() (string, error) {
	data, err := os.ReadFile("CHANGELOG.md")
	if err != nil {
		return "", err
	}
	match := regexp.MustCompile(`(?m)^## (v[01]\.(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)) — [0-9]{4}-[0-9]{2}-[0-9]{2}$`).FindSubmatch(data)
	if match == nil {
		return "", errors.New("changelog needs a stable release version")
	}
	return string(match[1]), nil
}

func environment(values map[string]string) []string {
	env := os.Environ()
	for key, value := range values {
		env = slices.DeleteFunc(env, func(s string) bool { return strings.HasPrefix(s, key+"=") })
		env = append(env, key+"="+value)
	}
	return env
}

func run(ctx context.Context, dir string, env map[string]string, capture bool, args ...string) (string, error) {
	fmt.Printf("+ %s [%s]\n", strings.Join(args, " "), dir)
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Dir, cmd.Env = dir, environment(env)
	if capture {
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	return "", cmd.Run()
}

func check(ctx context.Context, args []string) error {
	flags := flag.NewFlagSet("check", flag.ContinueOnError)
	short := flags.Bool("short", false, "fast checks without external PostgreSQL or coverage gate")
	sqliteOnly := flags.Bool("sqlite-only", false, "SQLite and executable checks without PostgreSQL")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *short && *sqliteOnly {
		return errors.New("invalid check arguments")
	}
	if !*short && !*sqliteOnly && os.Getenv("XMIGRATOR_TEST_PG_DSN") == "" {
		return errors.New("full checks require XMIGRATOR_TEST_PG_DSN pointing to a disposable PostgreSQL database")
	}
	if err := workspace(); err != nil {
		return err
	}
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	artifacts := filepath.Join(root, ".artifacts")
	if err := os.MkdirAll(artifacts, 0o755); err != nil {
		return err
	}
	binaryCov := filepath.Join(artifacts, "binary-cover")
	if err := os.RemoveAll(binaryCov); err != nil {
		return err
	}
	env := map[string]string{"XMIGRATOR_BINARY_COVER_DIR": binaryCov}
	var profiles []string
	for i, module := range modules {
		if _, err := run(ctx, module, env, false, "go", "vet", "./..."); err != nil {
			return err
		}
		profile := filepath.Join(artifacts, fmt.Sprintf("unit-%d.out", i))
		argv := []string{"go", "test", "./...", "-race", "-count=1", "-covermode=atomic", "-coverprofile=" + profile}
		if *short || *sqliteOnly {
			argv = append(argv, "-short")
		}
		if _, err := run(ctx, module, env, false, argv...); err != nil {
			return err
		}
		profiles = append(profiles, profile)
	}
	if !*short {
		profile := filepath.Join(artifacts, "integration.out")
		argv := []string{"go", "test", "./...", "-race", "-count=1", "-covermode=atomic", "-coverprofile=" + profile, "-coverpkg=" + prefix + "/..."}
		if *sqliteOnly {
			argv = append(argv, "-run", "TestSQLite|TestSQLBodies/SQLite|TestPanicRollsBackSQLAndHistory/SQLite")
		}
		if _, err := run(ctx, "internal/integration", env, false, argv...); err != nil {
			return err
		}
		profiles = append(profiles, profile)
		if _, err := run(ctx, "internal/integration", map[string]string{"GOWORK": "off"}, false, "go", "test", "-short", "./..."); err != nil {
			return err
		}
		if _, err := run(ctx, "cmd/xmigrator", env, false, "go", "test", "-race", "-count=1", "-run", "TestExecutable", "./"); err != nil {
			return err
		}
		profile = filepath.Join(artifacts, "binary.out")
		if _, err := run(ctx, ".", env, false, "go", "tool", "covdata", "textfmt", "-i="+binaryCov, "-o="+profile); err != nil {
			return err
		}
		profiles = append(profiles, profile)
	}
	for _, module := range []string{".", "driver/sqlite", "cli/urfavecli"} {
		deps, err := run(ctx, module, nil, true, "go", "list", "-deps", "./...")
		if err != nil {
			return fmt.Errorf("list dependencies: %w: %s", err, deps)
		}
		if strings.Contains(deps, "modernc.org/") {
			return fmt.Errorf("SQLite engine leaked into %s", module)
		}
	}
	manifest, err := os.ReadFile("go.mod")
	if err != nil {
		return err
	}
	if strings.Contains(string(manifest), "require") {
		return errors.New("core must have no external dependencies")
	}
	for _, module := range modules[1 : len(modules)-1] {
		manifest, err := os.ReadFile(filepath.Join(module, "go.mod"))
		if err != nil {
			return err
		}
		if strings.Contains(string(manifest), "replace ") {
			return fmt.Errorf("published manifest has replacements: %s", module)
		}
	}
	if err := mergeCoverage(profiles, filepath.Join(artifacts, "coverage.out"), !*short, *sqliteOnly); err != nil {
		return err
	}
	_, err = run(ctx, ".", nil, false, "go", "tool", "cover", "-func="+filepath.Join(artifacts, "coverage.out"))
	return err
}

type coverageBlock struct{ statements, count int64 }

func mergeCoverage(profiles []string, target string, gate, sqliteOnly bool) error {
	blocks := map[string]coverageBlock{}
	for _, profile := range profiles {
		file, err := os.Open(profile)
		if err != nil {
			return err
		}
		scanner := bufio.NewScanner(file)
		if !scanner.Scan() || scanner.Text() != "mode: atomic" {
			file.Close()
			return fmt.Errorf("incompatible coverage: %s", profile)
		}
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) != 3 {
				file.Close()
				return fmt.Errorf("invalid coverage: %s", profile)
			}
			if strings.Contains(fields[0], "/internal/contracttest/") {
				continue
			}
			n, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				file.Close()
				return err
			}
			count, err := strconv.ParseInt(fields[2], 10, 64)
			if err != nil {
				file.Close()
				return err
			}
			block, exists := blocks[fields[0]]
			if exists && block.statements != n {
				file.Close()
				return errors.New("coverage statement counts disagree")
			}
			blocks[fields[0]] = coverageBlock{n, block.count + count}
		}
		err = errors.Join(scanner.Err(), file.Close())
		if err != nil {
			return err
		}
	}
	keys := make([]string, 0, len(blocks))
	for key := range blocks {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var output strings.Builder
	output.WriteString("mode: atomic\n")
	stats := map[string][2]int64{}
	for _, key := range keys {
		b := blocks[key]
		fmt.Fprintf(&output, "%s %d %d\n", key, b.statements, b.count)
		file := strings.SplitN(key, ":", 2)[0]
		pkg := file[:strings.LastIndex(file, "/")]
		v := stats[pkg]
		v[0] += b.statements
		if b.count > 0 {
			v[1] += b.statements
		}
		stats[pkg] = v
	}
	if err := os.WriteFile(target, []byte(output.String()), 0o644); err != nil {
		return err
	}
	packages := make([]string, 0, len(stats))
	for pkg := range stats {
		packages = append(packages, pkg)
	}
	sort.Strings(packages)
	var failures []error
	for _, pkg := range packages {
		v := stats[pkg]
		percent := 100.0
		if v[0] > 0 {
			percent = 100 * float64(v[1]) / float64(v[0])
		}
		fmt.Printf("%s: %.1f%% (%d/%d)\n", pkg, percent, v[1], v[0])
		gated := gate && (!sqliteOnly || pkg != prefix+"/driver/pgx" && pkg != prefix+"/cmd/xmigrator/internal/app")
		if gated && v[0] != v[1] {
			failures = append(failures, fmt.Errorf("coverage gate failed: %s", pkg))
		}
	}
	return errors.Join(failures...)
}

func fileURL(path string) string {
	path = filepath.ToSlash(path)
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return (&url.URL{Scheme: "file", Path: path}).String()
}

func verifyInstall(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "go", "env", "GOMODCACHE")
	cacheBytes, err := cmd.Output()
	if err != nil {
		return err
	}
	cache := strings.TrimSpace(string(cacheBytes))
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	temp, err := os.MkdirTemp("", "xmigrator-install-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	proxy := filepath.Join(temp, "proxy")
	version, err := releaseVersion()
	if err != nil {
		return err
	}
	for _, module := range modules[:len(modules)-1] {
		source := filepath.Join(root, module)
		name := prefix
		if module != "." {
			name += "/" + module
		}
		versions := filepath.Join(proxy, filepath.FromSlash(name), "@v")
		if err := os.MkdirAll(versions, 0o755); err != nil {
			return err
		}
		manifest, err := os.ReadFile(filepath.Join(source, "go.mod"))
		if err != nil {
			return err
		}
		if strings.Contains(string(manifest), "replace ") {
			return fmt.Errorf("published manifest has replace: %s", name)
		}
		for suffix, content := range map[string][]byte{
			".mod":  manifest,
			".info": []byte(fmt.Sprintf(`{"Version":%q,"Time":"2026-10-01T00:00:00Z"}`, version)),
		} {
			if err := os.WriteFile(filepath.Join(versions, version+suffix), content, 0o644); err != nil {
				return err
			}
		}
		if err := os.WriteFile(filepath.Join(versions, "list"), []byte(version+"\n"), 0o644); err != nil {
			return err
		}
		if err := moduleZip(source, name+"@"+version, filepath.Join(versions, version+".zip")); err != nil {
			return err
		}
	}
	env := map[string]string{
		"GOWORK": "off", "GOPRIVATE": "", "GONOPROXY": "", "GONOSUMDB": prefix + "*", "GOSUMDB": "off",
		"GOPROXY":    fileURL(proxy) + "," + fileURL(filepath.Join(cache, "cache", "download")),
		"GOMODCACHE": filepath.Join(temp, "modcache"), "GOBIN": filepath.Join(temp, "bin"),
	}
	if _, err := run(ctx, temp, env, false, "go", "install", prefix+"/cmd/xmigrator@"+version); err != nil {
		return err
	}
	binary := "xmigrator"
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	_, err = run(ctx, temp, env, false, filepath.Join(temp, "bin", binary), "--version")
	return err
}

func moduleZip(source, name, target string) (err error) {
	file, err := os.Create(target)
	if err != nil {
		return err
	}
	archive := zip.NewWriter(file)
	defer func() { err = errors.Join(err, archive.Close(), file.Close()) }()
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == source {
			return nil
		}
		if entry.IsDir() {
			if strings.HasPrefix(entry.Name(), ".") || slices.Contains([]string{"build", "frontend", "node_modules"}, entry.Name()) {
				return filepath.SkipDir
			}
			if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.Type().IsRegular() || strings.HasPrefix(entry.Name(), ".") || slices.Contains([]string{"go.work", "go.work.sum"}, entry.Name()) {
			return nil
		}
		rel, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		writer, err := archive.Create(name + "/" + filepath.ToSlash(rel))
		if err != nil {
			return err
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		_, err = io.Copy(writer, input)
		return errors.Join(err, input.Close())
	})
}

type mutation struct{ name, file, before, after, target, test string }

func proveRegressions(ctx context.Context) error {
	if err := workspace(); err != nil {
		return err
	}
	mutations := []mutation{
		{"late_versions", "migrator.go", "if _, ok := applied[g.Version]; !ok {", "if _, ok := applied[g.Version]; !ok && g.Version>max {", "./internal/integration", "TestSQLiteContract/late_version_reverse_application_and_reapply"},
		{"down_order", "migrator.go", "cmp.Compare(b.ApplyOrder, a.ApplyOrder)", "cmp.Compare(b.Version, a.Version)", "./internal/integration", "TestSQLiteContract/late_version_reverse_application_and_reapply"},
		{"migration_name", "source.go", `return Version(v), m[2], Direction(m[3]), nil`, `return Version(v), "", Direction(m[3]), nil`, ".", "TestSource/SQL"},
		{"exclusive_creation", "create.go", "os.O_WRONLY|os.O_CREATE|os.O_EXCL", "os.O_WRONLY|os.O_CREATE|os.O_TRUNC", ".", "TestCreateSameNameConcurrentPreflight"},
		{"implicit_noop", "source.go", " || !hasSQL && s.Kind == DownSQL", "", ".", "TestSource/empty"},
		{"transaction_control", "source.go", `"BEGIN", "START", "COMMIT",`, `"START",`, "./internal/integration", "TestSQLiteContract/unsafe_script_is_rejected_before_effects"},
		{"foreign_key_rebuild", "driver/sqlite/sqlite.go", "fk = 0", "fk = 1", "./internal/integration", "TestSQLiteForeignKeyRebuild"},
	}
	if err := os.MkdirAll(filepath.Join(".artifacts", "mutations"), 0o755); err != nil {
		return err
	}
	for _, m := range mutations {
		if err := proveMutation(ctx, m); err != nil {
			return err
		}
		fmt.Printf("PROVED: %s — behavioral assertion rejected the broken implementation\n", m.name)
	}
	fmt.Println("All mutation sources restored.")
	return nil
}

func proveMutation(ctx context.Context, m mutation) (err error) {
	original, err := os.ReadFile(m.file)
	if err != nil {
		return err
	}
	if strings.Count(string(original), m.before) != 1 {
		return fmt.Errorf("mutation %s needs review: expected exactly one source anchor", m.name)
	}
	changed := strings.Replace(string(original), m.before, m.after, 1)
	if err := os.WriteFile(m.file, []byte(changed), 0o644); err != nil {
		return err
	}
	defer func() {
		current, readErr := os.ReadFile(m.file)
		if readErr != nil {
			err = errors.Join(err, readErr)
			return
		}
		if string(current) != changed {
			err = errors.Join(err, fmt.Errorf("%s changed during mutation; refusing to overwrite a concurrent edit", m.file))
			return
		}
		err = errors.Join(err, os.WriteFile(m.file, original, 0o644))
	}()
	output, testErr := run(ctx, ".", nil, true, "go", "test", m.target, "-count=1", "-run", "^"+m.test+"$")
	if err := os.WriteFile(filepath.Join(".artifacts", "mutations", m.name+".txt"), []byte(output), 0o644); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if testErr == nil || strings.Contains(output, "[build failed]") || !strings.Contains(output, "--- FAIL:") {
		return fmt.Errorf("regression %s was not proved; see .artifacts/mutations/%s.txt", m.name, m.name)
	}
	return nil
}
