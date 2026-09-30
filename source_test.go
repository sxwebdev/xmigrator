package xmigrator_test

import (
	"context"
	"errors"
	"io/fs"
	"testing"
	"testing/fstest"

	x "github.com/sxwebdev/xmigrator"
)

func TestSource(t *testing.T) {
	for _, tt := range []struct {
		name, up, down string
		wantErr        error
	}{
		{"SQL", "SELECT ';', $$ BEGIN; COMMIT; $$;", "SELECT 2;", nil},
		{"BOM_CRLF", "\ufeff-- xmigrator:noop\r\n", "-- xmigrator:noop\n", nil},
		{"irreversible", "SELECT 1;", "-- xmigrator:irreversible", nil},
		{"hook_noop", "-- xmigrator:hooks=r1\n-- xmigrator:noop\n", "-- xmigrator:noop", nil},
		{"FK_noop", "-- xmigrator:sqlite-foreign-keys=off\n-- xmigrator:noop", "-- xmigrator:sqlite-foreign-keys=on\nSELECT 1;", nil},
		{"block_header", "/* nested /* x */ comment */\n-- xmigrator:noop", "-- xmigrator:noop", nil},
		{"body_directive", "CREATE TRIGGER t AFTER INSERT ON a BEGIN\n-- xmigrator:unknown\n SELECT 1; END;", "SELECT 1;", nil},
		{"empty", "", "SELECT 1;", x.ErrInvalidSource},
		{"BOM_only", "\ufeff", "SELECT 1;", x.ErrInvalidSource},
		{"double_BOM", "\ufeff\ufeffSELECT 1;", "SELECT 1;", x.ErrInvalidSource},
		{"noop_SQL", "-- xmigrator:noop\nSELECT 1;", "SELECT 1;", x.ErrInvalidSource},
		{"up_irreversible", "-- xmigrator:irreversible", "SELECT 1;", x.ErrInvalidSource},
		{"irreversible_hook", "SELECT 1;", "-- xmigrator:irreversible\n-- xmigrator:hooks=r", x.ErrInvalidSource},
		{"irreversible_FK", "SELECT 1;", "-- xmigrator:irreversible\n-- xmigrator:sqlite-foreign-keys=on", x.ErrInvalidSource},
		{"irreversible_noop", "SELECT 1;", "-- xmigrator:irreversible\n-- xmigrator:noop", x.ErrInvalidSource},
		{"late_directive", "SELECT 1;\n-- xmigrator:noop", "SELECT 1;", x.ErrInvalidSource},
		{"prefix_case", "-- XMigrator:noop", "SELECT 1;", x.ErrInvalidSource},
		{"unknown", "-- xmigrator:other", "SELECT 1;", x.ErrInvalidSource},
		{"repeated", "-- xmigrator:noop\n-- xmigrator:noop", "SELECT 1;", x.ErrInvalidSource},
		{"noop_value", "-- xmigrator:noop=true", "SELECT 1;", x.ErrInvalidSource},
		{"irreversible_value", "SELECT 1;", "-- xmigrator:irreversible=true", x.ErrInvalidSource},
		{"hook_invalid", "-- xmigrator:hooks=bad value\nSELECT 1;", "SELECT 1;", x.ErrInvalidSource},
		{"hook_missing", "-- xmigrator:hooks\nSELECT 1;", "SELECT 1;", x.ErrInvalidSource},
		{"FK_invalid", "-- xmigrator:sqlite-foreign-keys=OFF\nSELECT 1;", "SELECT 1;", x.ErrInvalidSource},
		{"unclosed_comment", "/*", "SELECT 1;", x.ErrInvalidSource},
		{"unclosed_quote", "SELECT 'x", "SELECT 1;", x.ErrInvalidSource},
		{"unclosed_dollar", "SELECT $body$x", "SELECT 1;", x.ErrInvalidSource},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			files := fstest.MapFS{"001_name.up.sql": {Data: []byte(tt.up)}, "001_name.down.sql": {Data: []byte(tt.down)}}
			s, e := x.NewSource(files, ".")
			if e != nil {
				t.Fatal(e)
			}
			m, e := s.Snapshot(t.Context())
			if !errors.Is(e, tt.wantErr) {
				t.Fatalf("got %v, want %v", e, tt.wantErr)
			}
			if e == nil && (len(m) != 1 || m[0].Version != 1 || m[0].Name != "name") {
				t.Fatalf("snapshot %+v", m)
			}
		})
	}
}

func TestCatalogErrors(t *testing.T) {
	for _, tt := range []struct {
		name  string
		files fstest.MapFS
		dir   string
		want  error
	}{
		{"missing", fstest.MapFS{}, "missing", x.ErrInvalidSource},
		{"bad_filename", fstest.MapFS{"wrong.sql": {Data: []byte("SELECT 1")}}, ".", x.ErrInvalidSource},
		{"overflow", fstest.MapFS{"9223372036854775808_x.up.sql": {}}, ".", x.ErrInvalidSource},
		{"zero", fstest.MapFS{"0_x.up.sql": {}}, ".", x.ErrInvalidSource},
		{"missing_pair", fstest.MapFS{"1_x.up.sql": {Data: []byte("SELECT 1")}}, ".", x.ErrInvalidSource},
		{"different_names", fstest.MapFS{"1_x.up.sql": {Data: []byte("SELECT 1")}, "1_y.down.sql": {Data: []byte("SELECT 1")}}, ".", x.ErrInvalidSource},
		{"duplicate", fstest.MapFS{"1_x.up.sql": {Data: []byte("SELECT 1")}, "001_x.up.sql": {Data: []byte("SELECT 1")}}, ".", x.ErrInvalidSource},
		{"empty_ignored", fstest.MapFS{"README": {Data: []byte("text")}}, ".", nil},
		{"directory_sql", fstest.MapFS{"1_x.up.sql": {Mode: fs.ModeDir}}, ".", x.ErrInvalidSource},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, e := x.NewSource(tt.files, tt.dir)
			if e != nil {
				t.Fatal(e)
			}
			_, e = x.Validate(t.Context(), s)
			if !errors.Is(e, tt.want) {
				t.Fatalf("%v, want %v", e, tt.want)
			}
		})
	}
	for _, dir := range []string{"", "/absolute", "../escape"} {
		if _, e := x.NewSource(fstest.MapFS{}, dir); !errors.Is(e, x.ErrInvalidSource) {
			t.Fatal(e)
		}
	}
	if _, e := x.NewSource(nil, "."); !errors.Is(e, x.ErrInvalidSource) {
		t.Fatal(e)
	}
	if _, e := (x.Source{}).Snapshot(t.Context()); !errors.Is(e, x.ErrInvalidSource) {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	s, _ := x.NewSource(validFiles(), ".")
	if _, e := s.Snapshot(ctx); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}

func TestNormalizationAndSnapshotIsolation(t *testing.T) {
	files := validFiles()
	s, _ := x.NewSource(files, ".")
	first, e := s.Snapshot(t.Context())
	if e != nil {
		t.Fatal(e)
	}
	files["1_m1.up.sql"].Data = []byte("\ufeffSELECT 1;\r\n")
	a, _ := s.Snapshot(t.Context())
	files["1_m1.up.sql"].Data = []byte("SELECT 1;\n")
	b, _ := s.Snapshot(t.Context())
	if a[0].Up.Checksum != b[0].Up.Checksum || a[0].Up.SQL != b[0].Up.SQL || first[0].Up.SQL != "SELECT 1;" {
		t.Fatal("normalization or immutable snapshot failed")
	}
	files["1_m1.up.sql"].Data = []byte("SELECT '\ufeff';")
	c, _ := s.Snapshot(t.Context())
	if c[0].Up.SQL != "SELECT '\ufeff';" {
		t.Fatal("embedded BOM changed")
	}
}

func TestSQLValidation(t *testing.T) {
	for _, tt := range []struct {
		name, sql, dialect string
		want               error
	}{
		{"commit_begin", "COMMIT;BEGIN;", "pgx", x.ErrUnsafeSQL},
		{"end", "END TRANSACTION;", "sqlite", x.ErrUnsafeSQL},
		{"savepoint", "SAVEPOINT x;", "pgx", x.ErrUnsafeSQL},
		{"PG_atomic", "CREATE FUNCTION f() RETURNS INT LANGUAGE SQL BEGIN ATOMIC SELECT CASE WHEN true THEN 1 ELSE 0 END; END;SELECT 2;", "pgx", nil},
		{"trigger", "CREATE TRIGGER tr AFTER INSERT ON t BEGIN SELECT CASE WHEN 1 THEN 2 END; END;SELECT 3;", "sqlite", nil},
		{"dollar", "CREATE FUNCTION f() RETURNS INT AS $body$ BEGIN;COMMIT;END $body$ LANGUAGE plpgsql;", "pgx", nil},
		{"quoted", "SELECT 'COMMIT;', \"END\", `BEGIN`, [END]; -- COMMIT\nSELECT $1;", "pgx", nil},
		{"escaped", `SELECT E'a\'b;COMMIT;';`, "pgx", nil},
		{"concurrent", "CREATE INDEX CONCURRENTLY x ON t(a);", "pgx", x.ErrUnsafeSQL},
		{"vacuum", "VACUUM;", "sqlite", x.ErrUnsafeSQL},
		{"attach", "ATTACH 'a' AS x;", "sqlite", x.ErrUnsafeSQL},
		{"pragma_FK", "PRAGMA foreign_keys=off;", "sqlite", x.ErrUnsafeSQL},
		{"set_transaction", "SET TRANSACTION READ ONLY;", "pgx", x.ErrUnsafeSQL},
		{"set_local", "SET LOCAL lock_timeout='2s';", "pgx", nil},
		{"set_constraints", "SET CONSTRAINTS ALL DEFERRED;", "pgx", nil},
		{"local_quoted_parser", `SET LOCAL "standard_conforming_strings"=off;`, "pgx", x.ErrUnsafeSQL},
		{"reindex_options", "REINDEX (VERBOSE) INDEX CONCURRENTLY i;", "pgx", x.ErrUnsafeSQL},
		{"local_parser", "SET LOCAL standard_conforming_strings=off;", "pgx", x.ErrUnsafeSQL},
		{"local_replication", "SET LOCAL session_replication_role=replica;", "pgx", x.ErrUnsafeSQL},
		{"system_table", "CREATE TABLE system(id INTEGER);", "pgx", nil},
		{"transaction_column", "CREATE TABLE t(transaction INTEGER);", "sqlite", nil},
		{"system_schema", "CREATE SCHEMA system;", "pgx", nil},
		{"alter_system", "ALTER SYSTEM SET work_mem='1MB';", "pgx", x.ErrUnsafeSQL},
		{"pragma_attached", "PRAGMA main.foreign_keys=OFF;", "sqlite", x.ErrUnsafeSQL},
		{"drop_concurrent", "DROP INDEX CONCURRENTLY i;", "pgx", x.ErrUnsafeSQL},
		{"refresh_concurrent", "REFRESH MATERIALIZED VIEW CONCURRENTLY v;", "pgx", nil},
		{"reindex_concurrent", "REINDEX INDEX CONCURRENTLY i;", "pgx", x.ErrUnsafeSQL},
		{"ordinary_pragma", "PRAGMA user_version=1;", "sqlite", nil},
		{"set_parser", "SET standard_conforming_strings=off;", "pgx", x.ErrUnsafeSQL},
		{"bad_dialect", "SELECT 1;", "bad", x.ErrInvalidConfig},
		{"bad_quote", "SELECT '", "pgx", x.ErrUnsafeSQL},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			e := x.ValidateSQL(x.Script{SQL: tt.sql}, tt.dialect)
			if !errors.Is(e, tt.want) {
				t.Fatalf("%v, want %v", e, tt.want)
			}
		})
	}
	off := false
	if e := x.ValidateSQL(x.Script{ForeignKeys: &off}, "pgx"); !errors.Is(e, x.ErrUnsafeSQL) {
		t.Fatal(e)
	}
}

func FuzzSource(f *testing.F) {
	f.Add("SELECT 1;", "SELECT 2;")
	f.Add("-- xmigrator:noop", "-- xmigrator:irreversible")
	f.Fuzz(func(t *testing.T, up, down string) {
		files := fstest.MapFS{"1_x.up.sql": {Data: []byte(up)}, "1_x.down.sql": {Data: []byte(down)}}
		s, _ := x.NewSource(files, ".")
		m, e := s.Snapshot(t.Context())
		if e == nil {
			if len(m) != 1 || m[0].Version != 1 {
				t.Fatal("invalid successful snapshot")
			}
			files["1_x.up.sql"].Data = []byte(m[0].Up.SQL)
			files["1_x.down.sql"].Data = []byte(m[0].Down.SQL)
			again, e := s.Snapshot(t.Context())
			if e != nil || again[0].Up.Checksum != m[0].Up.Checksum || again[0].Down.Checksum != m[0].Down.Checksum {
				t.Fatal("normalization not idempotent")
			}
		}
	})
}

func TestQuotedFirstAndQuotedPragmas(t *testing.T) {
	if e := x.ValidateSQL(x.Script{SQL: "\"COMMIT\";"}, "pgx"); e != nil {
		t.Fatal(e)
	}
	for _, s := range []string{`PRAGMA "foreign_keys"=off;`, `PRAGMA [foreign_keys](off);`, `PRAGMA main.foreign_keys=off;`} {
		if e := x.ValidateSQL(x.Script{SQL: s}, "sqlite"); !errors.Is(e, x.ErrUnsafeSQL) {
			t.Fatalf("quoted safety pragma accepted: %s %v", s, e)
		}
	}
}

func TestQuotedSchemaIdentifiers(t *testing.T) {
	if e := x.ValidateSQL(x.Script{SQL: `CREATE TABLE t("transaction" TEXT,"concurrently" TEXT);`}, "pgx"); e != nil {
		t.Fatal(e)
	}
}

func TestDialectBoundaryTransactionControl(t *testing.T) {
	for _, s := range []string{
		`CREATE TABLE t$body$ (id INTEGER);COMMIT;CREATE TABLE t$body$x(id INTEGER);`,
		"SELECT 1;/* outer /* inner */ COMMIT; -- */",
		`SELECT E'\';COMMIT; -- ';`,
	} {
		if e := x.ValidateSQL(x.Script{SQL: s}, "sqlite"); !errors.Is(e, x.ErrUnsafeSQL) {
			t.Fatalf("dialect escape accepted: %s %v", s, e)
		}
	}
	if e := x.ValidateSQL(x.Script{SQL: "/* outer /* inner */ CREATE TABLE hidden(id INTEGER); -- */", Kind: x.DownNoop}, "sqlite"); !errors.Is(e, x.ErrUnsafeSQL) {
		t.Fatal(e)
	}
}

func TestCopyProtocol(t *testing.T) {
	for _, s := range []string{"COPY users FROM STDIN;", "COPY users TO STDOUT;"} {
		if e := x.ValidateSQL(x.Script{SQL: s}, "pgx"); !errors.Is(e, x.ErrUnsafeSQL) {
			t.Fatal(e)
		}
	}
	if e := x.ValidateSQL(x.Script{SQL: "COPY users FROM '/server/data.csv';"}, "pgx"); e != nil {
		t.Fatal(e)
	}
}

func TestPostgreSQLLexicalEdges(t *testing.T) {
	for _, s := range []string{"\vCOMMIT;", "SELECT 1;-- line\rCOMMIT;", "CREATE TABLE t$body$(id INTEGER);COMMIT;CREATE TABLE t$body$x(id INTEGER);"} {
		if e := x.ValidateSQL(x.Script{SQL: s}, "pgx"); !errors.Is(e, x.ErrUnsafeSQL) {
			t.Fatalf("control statement hidden by lexical edge: %s %v", s, e)
		}
	}
	if e := x.ValidateSQL(x.Script{SQL: "-- comment\rSELECT 1;", Kind: x.DownSQL}, "sqlite"); !errors.Is(e, x.ErrUnsafeSQL) {
		t.Fatal("implicit backend noop", e)
	}
}
