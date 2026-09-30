package pgx

import (
	"context"
	"errors"
	"testing"

	x "github.com/sxwebdev/xmigrator"
)

func TestInspect(t *testing.T) {
	for _, tt := range []struct {
		name string
		edit func(*fakeConnection)
		want error
	}{
		{"valid", func(*fakeConnection) {}, nil},
		{"partial", func(c *fakeConnection) { c.missingSuffix = "history" }, x.ErrMetadataConflict},
		{"view", func(c *fakeConnection) { c.kind = "v" }, x.ErrMetadataConflict},
		{"columns", func(c *fakeConnection) { c.columnsBad = true }, x.ErrMetadataConflict},
		{"constraints", func(c *fakeConnection) { c.constraintsBad = true }, x.ErrMetadataConflict},
		{"trigger", func(c *fakeConnection) { c.altered = true }, x.ErrMetadataConflict},
		{"owner", func(c *fakeConnection) { c.owner = "other" }, x.ErrMetadataConflict},
		{"format", func(c *fakeConnection) { c.format++ }, x.ErrMetadataVersion},
		{"id", func(c *fakeConnection) { c.id = 2 }, x.ErrMetadataConflict},
		{"count", func(c *fakeConnection) { c.count = 2 }, x.ErrMetadataConflict},
		{"class_query", func(c *fakeConnection) { c.queryFail = "c.relkind::text,c.oid" }, fault},
		{"columns_query", func(c *fakeConnection) { c.queryFail = "pg_attribute" }, fault},
		{"columns_scan", func(c *fakeConnection) { c.scanFail = "pg_attribute" }, fault},
		{"constraints_query", func(c *fakeConnection) { c.queryFail = "pg_get_constraintdef" }, fault},
		{"constraints_scan", func(c *fakeConnection) { c.scanFail = "pg_get_constraintdef" }, fault},
		{"marker_query", func(c *fakeConnection) { c.queryFail = "id,owner_id" }, fault},
		{"count_query", func(c *fakeConnection) { c.queryFail = "SELECT count(*)" }, fault},
		{"trigger_query", func(c *fakeConnection) { c.queryFail = "relrowsecurity" }, fault},
		{"rows_error", func(c *fakeConnection) { c.rowsFail = true }, fault},
	} {
		t.Run(tt.name, func(t *testing.T) {
			d, c := connectionFor(t)
			c.exists = true
			tt.edit(c)
			exists, e := d.inspect(t.Context(), c)
			if !errors.Is(e, tt.want) || (e == nil && !exists) {
				t.Fatalf("exists=%v err=%v", exists, e)
			}
		})
	}
}

func TestBootstrapAndHistory(t *testing.T) {
	d, c := connectionFor(t)
	s := &session{d, c}
	if e := s.EnsureMetadata(t.Context()); e != nil {
		t.Fatal(e)
	}
	c.exists = true
	if e := s.EnsureMetadata(t.Context()); e != nil {
		t.Fatal(e)
	}
	c.exists = false
	d.cfg.RequireExistingMetadata = true
	if e := s.EnsureMetadata(t.Context()); !errors.Is(e, x.ErrMetadataConflict) {
		t.Fatal(e)
	}
	d.cfg.RequireExistingMetadata = false
	for _, q := range []string{"CREATE SCHEMA", "CREATE TABLE", "INSERT INTO"} {
		c.execFail = q
		if e := s.EnsureMetadata(t.Context()); !errors.Is(e, fault) {
			t.Fatal(e)
		}
	}
	c.execFail = ""
	c.history = recordValues()
	r, e := d.readHistory(t.Context(), c)
	if e != nil || len(r) != 1 || r[0].Name != "users" {
		t.Fatalf("%+v %v", r, e)
	}
	c.history[0][2] = []byte{1}
	if _, e := d.readHistory(t.Context(), c); !errors.Is(e, x.ErrMetadataConflict) {
		t.Fatal(e)
	}
	c.history = recordValues()
	c.scanFail = "SELECT version"
	if _, e := d.readHistory(t.Context(), c); !errors.Is(e, fault) {
		t.Fatal(e)
	}
	c.scanFail = ""
	c.queryFail = "SELECT version"
	if _, e := d.readHistory(t.Context(), c); !errors.Is(e, fault) {
		t.Fatal(e)
	}
}

func TestSnapshot(t *testing.T) {
	for _, stage := range []string{"missing", "valid", "acquire", "begin", "inspect", "rollback", "close"} {
		t.Run(stage, func(t *testing.T) {
			d, c := connectionFor(t)
			switch stage {
			case "valid":
				c.exists = true
				c.history = recordValues()
			case "acquire":
				d.acquire = func(context.Context) (connection, error) { return nil, fault }
			case "begin":
				c.beginErr = fault
			case "inspect":
				c.queryFail = "c.relkind"
			case "rollback":
				c.rollbackErr = fault
			case "close":
				c.closeErr = fault
			}
			r, e := d.ReadHistorySnapshot(t.Context())
			if stage == "missing" || stage == "valid" {
				if e != nil {
					t.Fatal(e)
				}
				if stage == "valid" && len(r) != 1 {
					t.Fatal(r)
				}
			} else if !errors.Is(e, fault) {
				t.Fatal(e)
			}
		})
	}
}

func TestDrop(t *testing.T) {
	for _, stage := range []string{"success", "scope", "extension", "extension_query", "object_query", "object_scan", "object_rows", "savepoint", "release", "restricted", "rollback_savepoint", "history", "unknown_commit", "unsupported", "unsupported_query"} {
		t.Run(stage, func(t *testing.T) {
			d, c := connectionFor(t)
			s := &session{d, c}
			want := fault
			switch stage {
			case "success":
				want = nil
			case "unsupported":
				c.unsupported = true
				want = x.ErrDropScope
			case "unsupported_query":
				c.queryFail = "pg_operator"
			case "scope":
				d.cfg.DropSchemas = nil
				want = x.ErrDropScope
			case "extension":
				c.extension = true
				want = x.ErrDropScope
			case "extension_query":
				c.queryFail = "SELECT EXISTS"
			case "object_query":
				c.queryFail = "UNION ALL"
			case "object_scan":
				c.scanFail = "UNION ALL"
			case "object_rows":
				c.rowsFail = true
			case "savepoint":
				c.execFail = "SAVEPOINT xmigrator_drop"
			case "release":
				c.execFail = "RELEASE SAVEPOINT"
			case "restricted":
				c.execFail = "DROP"
				want = x.ErrDropScope
			case "rollback_savepoint":
				c.dropRollbackError = true
			case "history":
				c.execFail = "DELETE FROM"
			case "unknown_commit":
				c.commitErr = fault
				want = x.ErrCommitOutcomeUnknown
			}
			e := s.Drop(t.Context())

			if !errors.Is(e, want) {
				t.Fatalf("drop %s: %v", stage, e)
			}
			if stage == "success" && c.drops != 10 {
				t.Fatalf("drop effects=%d", c.drops)
			}
		})
	}
}

func TestBootstrapSchemaInspectionFailure(t *testing.T) {
	d, c := connectionFor(t)
	c.queryFail = "SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_namespace"
	s := &session{d, c}
	if err := s.EnsureMetadata(t.Context()); !errors.Is(err, fault) {
		t.Fatal(err)
	}
}
