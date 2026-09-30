package sqltext_test

import (
	"testing"

	"github.com/sxwebdev/xmigrator/internal/sqltext"
)

func TestScan(t *testing.T) {
	for _, tt := range []struct {
		name, sql  string
		valid      bool
		statements int
	}{
		{"empty", " \t\r\n", true, 0}, {"comments", "-- x\n/* y /* z */ */", true, 0}, {"quotes", "SELECT 'a''b', \"a\"\"b\", `a``b`, [a]]b];SELECT $1;", true, 2}, {"escaped", `SELECT E'a\'b';`, true, 1}, {"dollar", "SELECT $tag_1$x;y$tag_1$,$$hello$$,$;", true, 1}, {"semicolon", ";;SELECT 1;;", true, 1}, {"unicode", "SELECT \u03b1\u03b2\u03b3;", true, 1}, {"atomic", "CREATE OR REPLACE FUNCTION f() RETURNS INT LANGUAGE SQL BEGIN ATOMIC SELECT 1;SELECT CASE WHEN 1 THEN 2 END;END;SELECT 2;", true, 2}, {"procedure", "CREATE PROCEDURE p() LANGUAGE SQL BEGIN ATOMIC SELECT 1;END;", true, 1}, {"trigger", "CREATE TRIGGER t AFTER INSERT ON a BEGIN SELECT 1;END;", true, 1}, {"quote_error", "'x", false, 0}, {"comment_error", "/*", false, 0}, {"dollar_error", "$tag$x", false, 0}, {"quoted_semicolon", "SELECT ';'", true, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tokens, e := sqltext.Scan(tt.sql)
			if (e == nil) != tt.valid {
				t.Fatalf("scan %v", e)
			}
			if e == nil {
				st := sqltext.Statements(tokens)
				if len(st) != tt.statements {
					t.Fatalf("%d statements, want %d", len(st), tt.statements)
				}
				for _, tok := range tokens {
					if tok.Text != tt.sql[tok.Start:tok.End] {
						t.Fatal("token offsets")
					}
				}
			}
		})
	}
}

func FuzzScan(f *testing.F) {
	f.Add("SELECT 1;")
	f.Add("/* x */ CREATE TRIGGER t BEGIN SELECT CASE WHEN true THEN 1 END; END;")
	f.Fuzz(func(t *testing.T, s string) {
		tokens, e := sqltext.Scan(s)
		if e == nil {
			for _, tok := range tokens {
				if tok.Start < 0 || tok.End > len(s) || tok.Start >= tok.End || s[tok.Start:tok.End] != tok.Text {
					t.Fatal("invalid token bounds")
				}
			}
			_ = sqltext.Statements(tokens)
		}
	})
}

func TestSQLiteDialect(t *testing.T) {
	for _, tt := range []struct {
		name, sql string
		count     int
		valid     bool
	}{
		{"dollar_identifier", "CREATE TABLE t$body$ (id INTEGER);COMMIT;CREATE TABLE t$body$x(id INTEGER);", 3, true},
		{"nonnested_comment", "/* outer /* inner */ COMMIT; -- */", 1, true},
		{"brackets", "SELECT [x]];COMMIT;", 2, true},
		{"E_is_not_escape", `SELECT E'\';COMMIT; -- ';`, 2, true},
		{"unterminated_comment", "/*", 0, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			tokens, e := sqltext.ScanDialect(tt.sql, "sqlite")
			if (e == nil) != tt.valid {
				t.Fatal(e)
			}
			if e == nil && len(sqltext.Statements(tokens)) != tt.count {
				t.Fatalf("statements %+v", sqltext.Statements(tokens))
			}
		})
	}
}

func TestPostgreSQLBracketsArePunctuation(t *testing.T) {
	sql := "SELECT ARRAY[']'];"
	tokens, err := sqltext.ScanDialect(sql, "pgx")
	if err != nil {
		t.Fatal(err)
	}
	if len(sqltext.Statements(tokens)) != 1 {
		t.Fatal("array changed statement boundaries")
	}
	brackets := 0
	for _, token := range tokens {
		if token.Text == "[" || token.Text == "]" {
			if token.Quoted {
				t.Fatal("array bracket treated as a quote")
			}
			brackets++
		}
	}
	if brackets != 2 {
		t.Fatalf("array brackets missing: %d", brackets)
	}
}
