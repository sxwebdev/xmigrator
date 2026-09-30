package xmigrator_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"testing/fstest"

	x "github.com/sxwebdev/xmigrator"
)

func TestStatusReportsMissingAndMismatchedHooks(t *testing.T) {
	for _, tt := range []struct {
		name, up, down string
		hooks          map[x.Version]x.Hooks[*fakeDriver]
		kind           string
		direction      x.Direction
	}{
		{"missing_up", "-- xmigrator:hooks=seed-1\nSELECT 1;", "SELECT 1;", nil, "missing_hooks", x.Up},
		{"missing_down", "SELECT 1;", "-- xmigrator:hooks=seed-1\nSELECT 1;", nil, "missing_hooks", x.Down},
		{"revision_mismatch", "-- xmigrator:hooks=seed-1\nSELECT 1;", "SELECT 1;", map[x.Version]x.Hooks[*fakeDriver]{1: {UpRevision: "seed-2", AfterUp: func(context.Context, *fakeDriver) error { return nil }}}, "hook_registration_mismatch", x.Up},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			files := validFiles()
			files["1_m1.up.sql"].Data = []byte(tt.up)
			files["1_m1.down.sql"].Data = []byte(tt.down)
			source, err := x.NewSource(files, ".")
			if err != nil {
				t.Fatal(err)
			}
			driver := fake(t)
			runner, err := x.New(source, driver, x.WithHooks(tt.hooks))
			if err != nil {
				t.Fatal(err)
			}
			status, err := runner.Status(t.Context())
			if err != nil {
				t.Fatal(err)
			}
			if len(status.Issues) != 1 || status.Issues[0].Kind != tt.kind || status.Issues[0].Direction != tt.direction {
				t.Fatalf("missing hook diagnostics: %+v", status.Issues)
			}
			if driver.sessions != 0 || len(driver.records) != 0 {
				t.Fatal("status mutated the database")
			}
		})
	}
}

func TestMigrationErrorIdentifiesFailedStepAndPreservesCause(t *testing.T) {
	driver := fake(t)
	runner := makeMigrator(t, driver, validFiles())
	driver.fail = "exec"
	_, err := runner.Up(t.Context(), 0)
	migration, ok := errors.AsType[*x.MigrationError](err)
	if !ok || migration.Version != 1 || migration.Direction != x.Up || migration.Name != "m1" || migration.Stage != "sql" || !errors.Is(err, boundaryFailure) {
		t.Fatalf("failure has no migration context: %v", err)
	}
	if !strings.Contains(err.Error(), "m1") {
		t.Fatal("error lost migration name")
	}
}

func TestSourceErrorsExposeFilename(t *testing.T) {
	files := validFiles()
	files["1_other.up.sql"] = &fstest.MapFile{Data: []byte("SELECT 2;")}
	source, err := x.NewSource(files, ".")
	if err != nil {
		t.Fatal(err)
	}
	_, err = source.Snapshot(t.Context())
	failure, ok := errors.AsType[*x.SourceError](err)
	if !ok || failure.File == "" || failure.Version != 1 || !errors.Is(err, x.ErrInvalidSource) || !strings.Contains(err.Error(), failure.File) {
		t.Fatalf("source filename lost: %v", err)
	}
}

func TestSourceDialectOptions(t *testing.T) {
	for _, dialect := range []string{"pgx", "sqlite"} {
		t.Run(dialect, func(t *testing.T) {
			t.Parallel()
			source, err := x.NewSource(validFiles(), ".", x.WithDialect(dialect))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := source.Snapshot(t.Context()); err != nil {
				t.Fatal(err)
			}
		})
	}
	for _, option := range []x.SourceOption{nil, x.WithDialect("unsupported")} {
		if _, err := x.NewSource(validFiles(), ".", option); !errors.Is(err, x.ErrInvalidConfig) {
			t.Fatal(err)
		}
	}
}

func TestValidateRejectsUnsafeMigrationWithContext(t *testing.T) {
	for _, dialect := range []string{"pgx", "sqlite"} {
		t.Run(dialect, func(t *testing.T) {
			t.Parallel()
			files := validFiles()
			files["1_m1.up.sql"].Data = []byte("BEGIN;")
			source, err := x.NewSource(files, ".", x.WithDialect(dialect))
			if err != nil {
				t.Fatal(err)
			}
			_, err = x.Validate(t.Context(), source)
			failure, ok := errors.AsType[*x.MigrationError](err)
			if !ok || failure.Version != 1 || failure.Direction != x.Up || failure.Stage != "validation" || !errors.Is(err, x.ErrUnsafeSQL) {
				t.Fatalf("unsafe migration passed validation: %v", err)
			}
		})
	}
}

func TestPreflightErrorsCarryMigrationContext(t *testing.T) {
	for _, tt := range []struct {
		name   string
		edit   func(fstest.MapFS)
		policy x.UnknownAppliedPolicy
		want   error
		stage  string
	}{
		{"checksum", func(files fstest.MapFS) { files["1_m1.up.sql"].Data = []byte("SELECT 99;") }, x.UnknownAppliedAllow, x.ErrChecksumMismatch, "history_check"},
		{"unsafe", func(files fstest.MapFS) { files["1_m1.up.sql"].Data = []byte("BEGIN;") }, x.UnknownAppliedAllow, x.ErrUnsafeSQL, "validation"},
		{"unknown", func(files fstest.MapFS) { delete(files, "1_m1.up.sql"); delete(files, "1_m1.down.sql") }, x.UnknownAppliedError, x.ErrUnknownApplied, "planning"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			files := validFiles()
			driver := fake(t)
			runner := makeMigrator(t, driver, files, x.WithUnknownAppliedPolicy[*fakeDriver](tt.policy))
			if _, err := runner.Up(t.Context(), 0); err != nil {
				t.Fatal(err)
			}
			tt.edit(files)
			_, err := runner.Up(t.Context(), 0)
			failure, ok := errors.AsType[*x.MigrationError](err)
			if !ok || failure.Version != 1 || failure.Name != "m1" || failure.Direction != x.Up || failure.Stage != tt.stage || !errors.Is(err, tt.want) {
				t.Fatalf("preflight lost version: %v", err)
			}
		})
	}
}

func TestMissingPairCarriesSourceContext(t *testing.T) {
	files := validFiles()
	delete(files, "1_m1.down.sql")
	source, _ := x.NewSource(files, ".")
	_, err := source.Snapshot(t.Context())
	failure, ok := errors.AsType[*x.SourceError](err)
	if !ok || failure.Version != 1 || failure.File != "1_m1" || !errors.Is(err, x.ErrInvalidSource) {
		t.Fatalf("missing pair lost version: %v", err)
	}
}

func TestPostgreSQLArrayLiteral(t *testing.T) {
	files := validFiles()
	files["1_m1.up.sql"].Data = []byte("SELECT ARRAY[']'];")
	source, _ := x.NewSource(files, ".")
	if _, err := x.Validate(t.Context(), source); err != nil {
		t.Fatal(err)
	}
}

func TestUnknownCommitSentinelIsNotDuplicated(t *testing.T) {
	driver := fake(t)
	driver.fail = "commit_wrapped"
	runner := makeMigrator(t, driver, validFiles())
	_, err := runner.Up(t.Context(), 0)
	if !errors.Is(err, x.ErrCommitOutcomeUnknown) || strings.Count(err.Error(), x.ErrCommitOutcomeUnknown.Error()) != 1 {
		t.Fatalf("duplicated commit diagnostic: %v", err)
	}
}

func TestSourceDialectCopyPreservesOriginal(t *testing.T) {
	source, _ := x.NewSource(validFiles(), ".")
	if source.Dialect() != "" {
		t.Fatal("unspecified dialect was silently selected")
	}
	copy, err := source.WithDialect("sqlite")
	if err != nil {
		t.Fatal(err)
	}
	if copy.Dialect() != "sqlite" || source.Dialect() != "" {
		t.Fatal("dialect selection mutated the source")
	}
	if _, err := source.WithDialect("invalid"); !errors.Is(err, x.ErrInvalidConfig) {
		t.Fatal(err)
	}
}

func TestDownValidationErrorCarriesDirection(t *testing.T) {
	files := validFiles()
	files["1_m1.down.sql"].Data = []byte("BEGIN;")
	runner := makeMigrator(t, fake(t), files)
	_, err := runner.Up(t.Context(), 0)
	failure, ok := errors.AsType[*x.MigrationError](err)
	if !ok || failure.Version != 1 || failure.Direction != x.Down || failure.Stage != "validation" || !errors.Is(err, x.ErrUnsafeSQL) {
		t.Fatalf("down validation lost direction: %v", err)
	}
}
