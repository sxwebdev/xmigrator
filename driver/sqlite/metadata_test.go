package sqlite

import (
	"database/sql/driver"
	"errors"
	"testing"
	"time"

	x "github.com/sxwebdev/xmigrator"
)

func TestMetadataInspection(t *testing.T) {
	for _, tt := range []struct {
		name  string
		setup func(*fakeEngine)
		want  error
	}{
		{"valid", func(f *fakeEngine) {}, nil},
		{"trigger", func(f *fakeEngine) { f.metadataAltered = true }, x.ErrMetadataConflict},
		{"trigger_query", func(f *fakeEngine) { f.failQuery = "SELECT EXISTS" }, fault},
		{"partial", func(f *fakeEngine) { delete(f.definitions, "__xmigrator_meta") }, x.ErrMetadataConflict},
		{"view", func(f *fakeEngine) { f.kind = "view" }, x.ErrMetadataConflict},
		{"structure", func(f *fakeEngine) { f.badDDL = true }, x.ErrMetadataConflict},
		{"owner", func(f *fakeEngine) { f.owner = "other" }, x.ErrMetadataConflict},
		{"format", func(f *fakeEngine) { f.format++ }, x.ErrMetadataVersion},
		{"id", func(f *fakeEngine) { f.id = 2 }, x.ErrMetadataConflict},
		{"count", func(f *fakeEngine) { f.count = 2 }, x.ErrMetadataConflict},
		{"schema_query", func(f *fakeEngine) { f.failQuery = "SELECT type,sql" }, fault},
		{"count_query", func(f *fakeEngine) { f.failQuery = "SELECT count(*)" }, x.ErrMetadataConflict},
		{"marker_query", func(f *fakeEngine) { f.failQuery = "SELECT id,owner_id" }, x.ErrMetadataConflict},
		{"rows_error", func(f *fakeEngine) { f.rowsErr = true }, fault},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d, f := engine(t)
			f.exists = true
			tt.setup(f)
			s := sessionFor(t, d)
			exists, e := s.inspect(t.Context())
			if !errors.Is(e, tt.want) || (e == nil && !exists) {
				t.Fatalf("exists=%v error=%v", exists, e)
			}
		})
	}
}

func TestBootstrapAndSnapshot(t *testing.T) {
	d, f := engine(t)
	if r, e := d.ReadHistorySnapshot(t.Context()); e != nil || len(r) != 0 {
		t.Fatalf("missing metadata: %v %v", r, e)
	}
	s := sessionFor(t, d)
	if e := s.EnsureMetadata(t.Context()); e != nil {
		t.Fatal(e)
	}
	f.exists = true
	if e := s.EnsureMetadata(t.Context()); e != nil {
		t.Fatal(e)
	}
	f.failExec = "CREATE TABLE"
	f.exists = false
	if e := s.EnsureMetadata(t.Context()); !errors.Is(e, fault) {
		t.Fatal(e)
	}
	f.failExec = "INSERT INTO"
	if e := s.EnsureMetadata(t.Context()); !errors.Is(e, fault) {
		t.Fatal(e)
	}
}

func TestHistoryDecoding(t *testing.T) {
	for _, tt := range []struct {
		name string
		edit func([]driver.Value)
		want error
	}{
		{"valid", func([]driver.Value) {}, nil}, {"short_up", func(r []driver.Value) { r[2] = []byte{1} }, x.ErrMetadataConflict}, {"short_down", func(r []driver.Value) { r[3] = []byte{1} }, x.ErrMetadataConflict}, {"time", func(r []driver.Value) { r[7] = "bad" }, x.ErrMetadataConflict}, {"scan", func(r []driver.Value) { r[0] = "bad" }, nil},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d, f := engine(t)
			r := []driver.Value{int64(1), "users", make([]byte, 32), make([]byte, 32), "sql", "", "", time.Now().UTC().Format(time.RFC3339Nano), int64(1)}
			tt.edit(r)
			f.history = [][]driver.Value{r}
			s := sessionFor(t, d)
			records, e := s.ReadHistory(t.Context())
			if tt.name == "scan" {
				if e == nil {
					t.Fatal("bad scan accepted")
				}
				return
			}
			if !errors.Is(e, tt.want) {
				t.Fatal(e)
			}
			if e == nil && (len(records) != 1 || records[0].Name != "users") {
				t.Fatal(records)
			}
		})
	}
	d, f := engine(t)
	s := sessionFor(t, d)
	f.rowsErr = true
	if _, e := s.ReadHistory(t.Context()); !errors.Is(e, fault) {
		t.Fatal(e)
	}
}

func TestDropFailures(t *testing.T) {
	for _, stage := range []string{"query", "rows", "drop", "history"} {
		t.Run(stage, func(t *testing.T) {
			d, f := engine(t)
			switch stage {
			case "query":
				f.failQuery = "SELECT type,name"
			case "rows":
				f.rowsErr = true
			case "drop":
				f.failExec = "DROP TABLE"
			case "history":
				f.failExec = "DELETE FROM"
			}
			s := sessionFor(t, d)
			if e := s.Drop(t.Context()); !errors.Is(e, fault) {
				t.Fatal(e)
			}
		})
	}
	d, _ := engine(t)
	s := sessionFor(t, d)
	if e := s.Drop(t.Context()); e != nil {
		t.Fatal(e)
	}
}

func TestMetadataScanAndSnapshotFailures(t *testing.T) {
	for _, query := range []string{"SELECT type,sql", "SELECT id,owner_id", "SELECT count(*)"} {
		t.Run(query, func(t *testing.T) {
			d, f := engine(t)
			f.exists = true
			f.badQuery = query
			if _, e := d.ReadHistorySnapshot(t.Context()); e == nil {
				t.Fatal("malformed metadata accepted")
			}
		})
	}
	for _, exec := range []string{"BEGIN", "ROLLBACK"} {
		d, f := engine(t)
		f.failExec = exec
		if _, e := d.ReadHistorySnapshot(t.Context()); !errors.Is(e, fault) {
			t.Fatal(e)
		}
	}
	d, f := engine(t)
	f.exists = true
	if _, e := d.ReadHistorySnapshot(t.Context()); e != nil {
		t.Fatal(e)
	}
	d, f = engine(t)
	f.badQuery = "SELECT type,name"
	s := sessionFor(t, d)
	if e := s.Drop(t.Context()); e == nil {
		t.Fatal("invalid object rows accepted")
	}
	d, f = engine(t)
	f.failExec = "COMMIT"
	f.failRollback = true
	s = sessionFor(t, d)
	if e := s.Drop(t.Context()); !errors.Is(e, x.ErrCommitOutcomeUnknown) {
		t.Fatal(e)
	}
}

func TestDDLFormatting(t *testing.T) {
	for _, a := range []string{`create table if not exists "x" ("id" integer);`, `CREATE TABLE [x](\u0069d INTEGER)`, `CREATE TABLE ` + "`x`(`id` INTEGER)"} {
		if a == `CREATE TABLE [x](\u0069d INTEGER)` {
			a = `CREATE TABLE [x](id INTEGER)`
		}
		if !sameDDL(a, "CREATE TABLE x(id INTEGER)") {
			t.Fatal(a)
		}
	}
	if sameDDL("/*", "CREATE TABLE x(id INTEGER)") || sameDDL("CREATE TABLE x(id INTEGER)", "/*") {
		t.Fatal("malformed DDL accepted")
	}
	if !sameDDL("CREATE TABLE x(kind TEXT CHECK(kind='sql'))", "create table \"x\"(kind text check(kind='sql'))") {
		t.Fatal("string values changed")
	}
}
