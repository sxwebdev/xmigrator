package xmigrator

import (
	"cmp"
	"context"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"path"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/sxwebdev/xmigrator/internal/sqltext"
)

type Source struct {
	files   fs.FS
	dir     string
	dialect string
}

type SourceOption func(*Source) error

// WithDialect explicitly selects lexical rules for source parsing.
// An unspecified source dialect falls back to pgx in Snapshot and Validate;
// built-in runners infer the driver dialect, while the CLI adapter requires an explicit dialect for validation.
func WithDialect(dialect string) SourceOption {
	return func(s *Source) error {
		if dialect != "pgx" && dialect != "sqlite" {
			return ErrInvalidConfig
		}
		s.dialect = dialect
		return nil
	}
}

func NewSource(files fs.FS, dir string, options ...SourceOption) (Source, error) {
	if isNil(files) || !fs.ValidPath(dir) {
		return Source{}, ErrInvalidSource
	}
	s := Source{files: files, dir: dir, dialect: ""}
	for _, option := range options {
		if option == nil {
			return Source{}, ErrInvalidConfig
		}
		if err := option(&s); err != nil {
			return Source{}, err
		}
	}
	return s, nil
}

var (
	filename = regexp.MustCompile(`^([0-9]+)_([a-z][a-z0-9]*(?:_[a-z0-9]+)*)\.(up|down)\.sql$`)
	revision = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)
)

func parseFilename(name string) (Version, string, Direction, error) {
	m := filename.FindStringSubmatch(name)
	if m == nil {
		return 0, "", "", fmt.Errorf("%w: filename %q", ErrInvalidSource, name)
	}
	v, e := strconv.ParseInt(m[1], 10, 64)
	if e != nil || v <= 0 {
		return 0, "", "", fmt.Errorf("%w: version in %q", ErrInvalidSource, name)
	}
	return Version(v), m[2], Direction(m[3]), nil
}

func (s Source) Snapshot(ctx context.Context) ([]Migration, error) {
	if isNil(s.files) {
		return nil, ErrInvalidSource
	}
	entries, err := fs.ReadDir(s.files, s.dir)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrInvalidSource, err)
	}
	type pair struct {
		migration Migration
		up, down  bool
	}
	pairs := map[Version]*pair{}
	for _, e := range entries {
		if err = ctx.Err(); err != nil {
			return nil, err
		}
		if !strings.HasSuffix(e.Name(), ".sql") {
			continue
		}
		v, n, d, err := parseFilename(e.Name())
		if err != nil {
			return nil, &SourceError{File: e.Name(), Err: err}
		}
		p := pairs[v]
		if p == nil {
			p = &pair{migration: Migration{Version: v, Name: n}}
			pairs[v] = p
		}
		if p.migration.Name != n || d == Up && p.up || d == Down && p.down {
			return nil, &SourceError{File: e.Name(), Version: v, Err: fmt.Errorf("%w: duplicate version %d", ErrInvalidSource, v)}
		}
		data, err := fs.ReadFile(s.files, path.Join(s.dir, e.Name()))
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrInvalidSource, err)
		}
		dialect := s.dialect
		if dialect == "" {
			dialect = "pgx"
		}
		script, err := parseScriptForDialect(string(data), d, dialect)
		if err != nil {
			return nil, &SourceError{File: e.Name(), Version: v, Err: err}
		}
		if d == Up {
			p.up = true
			p.migration.Up = script
		} else {
			p.down = true
			p.migration.Down = script
		}
	}
	out := make([]Migration, 0, len(pairs))
	for v, p := range pairs {
		if !p.up || !p.down {
			return nil, &SourceError{File: fmt.Sprintf("%d_%s", v, p.migration.Name), Version: v, Err: fmt.Errorf("%w: missing pair", ErrInvalidSource)}
		}
		out = append(out, p.migration)
	}
	slices.SortFunc(out, func(a, b Migration) int { return cmp.Compare(a.Version, b.Version) })
	return out, ctx.Err()
}

func Validate(ctx context.Context, s Source) (ValidationReport, error) {
	m, err := s.Snapshot(ctx)
	if err != nil {
		return ValidationReport{}, err
	}
	dialect := s.dialect
	if dialect == "" {
		dialect = "pgx"
	}
	for _, g := range m {
		for _, entry := range []struct {
			script    Script
			direction Direction
		}{{g.Up, Up}, {g.Down, Down}} {
			if err := ValidateSQL(entry.script, dialect); err != nil {
				return ValidationReport{}, &MigrationError{Version: g.Version, Name: g.Name, Direction: entry.direction, Stage: "validation", Err: err}
			}
		}
	}
	return ValidationReport{len(m)}, nil
}

func parseScriptForDialect(text string, direction Direction, dialect string) (Script, error) {
	text = strings.TrimPrefix(text, "\ufeff")
	if strings.HasPrefix(text, "\ufeff") {
		return Script{}, fmt.Errorf("%w: repeated BOM", ErrInvalidSource)
	}
	text = strings.ReplaceAll(text, "\r\n", "\n")
	tokens, err := sqltext.ScanDialect(text, dialect)
	if err != nil {
		return Script{}, fmt.Errorf("%w: %w", ErrInvalidSource, err)
	}
	s := Script{SQL: text, Checksum: sha256.Sum256([]byte(text)), Kind: DownSQL}
	seen := map[string]bool{}
	header := true
	hasSQL := false
	// Only standalone line comments in the header can declare directives.
	for _, t := range tokens {
		if !t.Comment {
			if t.Text != ";" {
				hasSQL = true
			}
			header = false
			continue
		}
		if !strings.HasPrefix(t.Text, "--") {
			continue
		}
		value := strings.Trim(t.Text[2:], " \t")
		if !strings.HasPrefix(strings.ToLower(value), "xmigrator:") {
			continue
		}
		if !header { // Comments inside a function/trigger body aren't header directives.
			inside := false
			statements := sqltext.Statements(tokens)
			for _, st := range statements {
				if len(st) > 0 && strings.EqualFold(st[0].Text, "CREATE") && t.Start > st[0].Start && t.End < st[len(st)-1].End {
					for _, q := range st {
						if strings.EqualFold(q.Text, "FUNCTION") || strings.EqualFold(q.Text, "PROCEDURE") || strings.EqualFold(q.Text, "TRIGGER") {
							inside = true
							break
						}
					}
					if inside {
						break
					}
				}
			}
			if inside {
				continue
			}
			return Script{}, fmt.Errorf("%w: directive outside header", ErrInvalidSource)
		}
		lineStart := strings.LastIndexAny(text[:t.Start], "\r\n") + 1
		if strings.Trim(text[lineStart:t.Start], " \t") != "" || !strings.HasPrefix(value, "xmigrator:") {
			return Script{}, fmt.Errorf("%w: directive prefix", ErrInvalidSource)
		}
		directive := strings.TrimPrefix(value, "xmigrator:")
		key, val, has := strings.Cut(directive, "=")
		if seen[key] {
			return Script{}, fmt.Errorf("%w: repeated directive", ErrInvalidSource)
		}
		seen[key] = true
		switch key {
		case "noop":
			if has {
				return Script{}, ErrInvalidSource
			}
			s.Kind = DownNoop
		case "irreversible":
			if has || direction != Down {
				return Script{}, ErrInvalidSource
			}
			s.Kind = DownIrreversible
		case "hooks":
			if !has || !revision.MatchString(val) {
				return Script{}, ErrInvalidSource
			}
			s.HookRevision = val
		case "sqlite-foreign-keys":
			if !has || (val != "on" && val != "off") {
				return Script{}, ErrInvalidSource
			}
			on := val == "on"
			s.ForeignKeys = &on
		default:
			return Script{}, fmt.Errorf("%w: unknown directive", ErrInvalidSource)
		}
	}
	if hasSQL && s.Kind != DownSQL || !hasSQL && s.Kind == DownSQL || seen["noop"] && seen["irreversible"] || s.Kind == DownIrreversible && (s.HookRevision != "" || s.ForeignKeys != nil) {
		return Script{}, fmt.Errorf("%w: SQL/directive combination", ErrInvalidSource)
	}
	return s, nil
}

// ValidateSQL enforces driver transaction ownership using dialect-aware statement boundaries.
func ValidateSQL(s Script, dialect string) error {
	if dialect != "sqlite" && dialect != "pgx" {
		return ErrInvalidConfig
	}
	if dialect == "pgx" && s.ForeignKeys != nil {
		return fmt.Errorf("%w: SQLite directive in PostgreSQL", ErrUnsafeSQL)
	}
	tokens, err := sqltext.ScanDialect(s.SQL, dialect)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrUnsafeSQL, err)
	}
	statements := sqltext.Statements(tokens)
	if (s.Kind == DownNoop || s.Kind == DownIrreversible) && len(statements) > 0 || s.Kind == DownSQL && len(statements) == 0 {
		return ErrUnsafeSQL
	}
	for _, st := range statements {
		if st[0].Quoted {
			continue
		}
		first := strings.ToUpper(st[0].Text)
		switch first {
		case "SET":
			if dialect == "pgx" && len(st) > 1 && !st[1].Quoted && (strings.EqualFold(st[1].Text, "LOCAL") || strings.EqualFold(st[1].Text, "CONSTRAINTS")) {
				// Changing lexical/session ownership settings remains unsafe even with LOCAL.
				if strings.EqualFold(st[1].Text, "LOCAL") && len(st) > 2 && (strings.EqualFold(strings.Trim(st[2].Text, "\""), "standard_conforming_strings") || strings.EqualFold(strings.Trim(st[2].Text, "\""), "session_replication_role")) {
					return ErrUnsafeSQL
				}
				continue
			}
			return fmt.Errorf("%w: SET", ErrUnsafeSQL)
		case "RESET", "BEGIN", "START", "COMMIT", "END", "ROLLBACK", "ABORT", "SAVEPOINT", "RELEASE", "PREPARE", "VACUUM", "ATTACH", "DETACH", "DISCARD":
			return fmt.Errorf("%w: %s", ErrUnsafeSQL, first)
		}
		if first == "COPY" {
			for _, t := range st {
				if !t.Quoted && (strings.EqualFold(t.Text, "STDIN") || strings.EqualFold(t.Text, "STDOUT")) {
					return ErrUnsafeSQL
				}
			}
		}
		if dialect == "pgx" {
			second := ""
			if len(st) > 1 && !st[1].Quoted {
				second = strings.ToUpper(st[1].Text)
			}
			if first == "ALTER" && second == "SYSTEM" {
				return ErrUnsafeSQL
			}
			// CONCURRENTLY is a modifier immediately after INDEX (or optional IF EXISTS),
			// or near the start of REINDEX. Concurrent materialized view refresh is transactional.
			for i, tok := range st {
				if tok.Quoted || !strings.EqualFold(tok.Text, "CONCURRENTLY") {
					continue
				}
				unsafe := first == "CREATE" && i > 0 && strings.EqualFold(st[i-1].Text, "INDEX") || first == "DROP" && second == "INDEX" && i == 2 || first == "REINDEX" && i > 0 && slices.Contains([]string{"INDEX", "TABLE", "SCHEMA", "DATABASE", "SYSTEM"}, strings.ToUpper(st[i-1].Text))
				if unsafe {
					return ErrUnsafeSQL
				}
			}
		}
		if first == "PRAGMA" {
			i := 1
			if len(st) > 3 && st[2].Text == "." {
				i = 3
			}
			if i < len(st) {
				name := strings.ToUpper(strings.Trim(st[i].Text, "\"`[]"))
				switch name {
				case "FOREIGN_KEYS", "DEFER_FOREIGN_KEYS", "JOURNAL_MODE", "WRITABLE_SCHEMA", "IGNORE_CHECK_CONSTRAINTS", "BUSY_TIMEOUT":
					return ErrUnsafeSQL
				}
			}
		}
	}
	return nil
}

// Dialect returns the explicitly configured dialect, or an empty string when unspecified.
func (s Source) Dialect() string { return s.dialect }

// WithDialect returns a copy with the selected SQL dialect.
func (s Source) WithDialect(dialect string) (Source, error) {
	err := WithDialect(dialect)(&s)
	return s, err
}
