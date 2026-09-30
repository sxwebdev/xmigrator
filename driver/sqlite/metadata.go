package sqlite

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	x "github.com/sxwebdev/xmigrator"
	"github.com/sxwebdev/xmigrator/internal/metadata"
	"github.com/sxwebdev/xmigrator/internal/sqltext"
)

func (d *Driver) definitions() map[string]string {
	return map[string]string{
		d.prefix + "meta":    "CREATE TABLE " + quote(d.prefix+"meta") + " (id INTEGER PRIMARY KEY CHECK(id=1), owner_id TEXT NOT NULL, format_version INTEGER NOT NULL)",
		d.prefix + "history": "CREATE TABLE " + quote(d.prefix+"history") + " (apply_order INTEGER PRIMARY KEY AUTOINCREMENT, version INTEGER NOT NULL UNIQUE CHECK(version>0), name TEXT NOT NULL, up_checksum BLOB NOT NULL CHECK(length(up_checksum)=32), down_checksum BLOB NOT NULL CHECK(length(down_checksum)=32), down_kind TEXT NOT NULL CHECK(down_kind IN ('sql','noop','irreversible')), up_hook_revision TEXT NOT NULL, down_hook_revision TEXT NOT NULL, applied_at TEXT NOT NULL)",
	}
}

func (s *session) inspect(ctx context.Context) (bool, error) {
	definitions := s.d.definitions()
	found := 0
	for name, expected := range definitions {
		rows, e := s.c.QueryContext(ctx, "SELECT type,sql FROM main.sqlite_schema WHERE name=?", name)
		if e != nil {
			return false, e
		}
		for rows.Next() {
			var kind, ddl string
			if e = rows.Scan(&kind, &ddl); e != nil {
				break
			}
			found++
			if kind != "table" || !sameDDL(ddl, expected) {
				e = x.ErrMetadataConflict
				break
			}
		}
		e = errors.Join(e, rows.Err(), rows.Close())
		if e != nil {
			return false, e
		}
	}
	if found == 0 {
		return false, nil
	}
	if found != 2 {
		return false, x.ErrMetadataConflict
	}
	var altered bool
	if e := s.c.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM main.sqlite_schema WHERE type='trigger' AND tbl_name IN (?,?))", s.d.prefix+"history", s.d.prefix+"meta").Scan(&altered); e != nil {
		return false, e
	}
	if altered {
		return false, x.ErrMetadataConflict
	}
	var count int
	if e := s.c.QueryRowContext(ctx, "SELECT count(*) FROM "+s.d.table("meta")).Scan(&count); e != nil {
		return false, errors.Join(x.ErrMetadataConflict, e)
	}
	if count != 1 {
		return false, x.ErrMetadataConflict
	}
	var owner string
	var format, id int
	if e := s.c.QueryRowContext(ctx, "SELECT id,owner_id,format_version FROM "+s.d.table("meta")).Scan(&id, &owner, &format); e != nil {
		return false, errors.Join(x.ErrMetadataConflict, e)
	}
	if id != 1 || owner != metadata.Owner {
		return false, x.ErrMetadataConflict
	}
	if format != metadata.Format {
		return false, x.ErrMetadataVersion
	}
	return true, nil
}

func (s *session) EnsureMetadata(ctx context.Context) error {
	_, e := s.InTx(ctx, x.Script{Kind: x.DownNoop}, func(x.Transaction[Tx]) error {
		exists, e := s.inspect(ctx)
		if e != nil || exists {
			return e
		}
		for _, ddl := range s.d.definitions() {
			if _, e = s.c.ExecContext(ctx, ddl); e != nil {
				return e
			}
		}
		_, e = s.c.ExecContext(ctx, "INSERT INTO "+s.d.table("meta")+" VALUES(1,?,?)", metadata.Owner, metadata.Format)
		return e
	})
	return e
}

func (s *session) ReadHistory(ctx context.Context) (out []x.Record, err error) {
	rows, err := s.c.QueryContext(ctx, "SELECT version,name,up_checksum,down_checksum,down_kind,up_hook_revision,down_hook_revision,applied_at,apply_order FROM "+s.d.table("history")+" ORDER BY apply_order")
	if err != nil {
		return nil, err
	}
	defer func() { err = errors.Join(err, rows.Close()) }()
	for rows.Next() {
		var r x.Record
		var up, down []byte
		var at string
		if err = rows.Scan(&r.Version, &r.Name, &up, &down, &r.DownKind, &r.UpHookRevision, &r.DownHookRevision, &at, &r.ApplyOrder); err != nil {
			return nil, err
		}
		if len(up) != 32 || len(down) != 32 {
			return nil, x.ErrMetadataConflict
		}
		copy(r.UpChecksum[:], up)
		copy(r.DownChecksum[:], down)
		r.AppliedAt, err = time.Parse(time.RFC3339Nano, at)
		if err != nil {
			return nil, errors.Join(x.ErrMetadataConflict, err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *session) Drop(ctx context.Context) error {
	off := false
	out, e := s.InTx(ctx, x.Script{Kind: x.DownNoop, ForeignKeys: &off}, func(tx x.Transaction[Tx]) error {
		rows, e := s.c.QueryContext(ctx, "SELECT type,name FROM main.sqlite_schema WHERE type IN ('table','view','trigger') AND name NOT GLOB 'sqlite_*' AND name NOT IN (?,?) AND name NOT IN (SELECT name FROM pragma_table_list WHERE schema='main' AND type='shadow') ORDER BY CASE type WHEN 'trigger' THEN 0 WHEN 'view' THEN 1 ELSE 2 END", s.d.prefix+"history", s.d.prefix+"meta")
		if e != nil {
			return e
		}
		type object struct{ kind, name string }
		var objects []object
		for rows.Next() {
			var o object
			if e = rows.Scan(&o.kind, &o.name); e != nil {
				break
			}
			objects = append(objects, o)
		}
		e = errors.Join(e, rows.Err(), rows.Close())
		if e != nil {
			return e
		}
		// Virtual table shadow tables can disappear when their owner is dropped.
		for _, o := range objects {
			if _, e = s.c.ExecContext(ctx, fmt.Sprintf("DROP %s IF EXISTS main.%s", strings.ToUpper(o.kind), quote(o.name))); e != nil {
				return e
			}
		}
		_, e = s.c.ExecContext(ctx, "DELETE FROM "+s.d.table("history"))
		return e
	})
	if out == x.TxUnknown {
		return errors.Join(x.ErrCommitOutcomeUnknown, e)
	}
	return e
}

// SQLite preserves the spelling of DDL in sqlite_schema. Compare token structure,
// allowing cosmetic formatting and identifier quoting without accepting missing constraints.
func sameDDL(a, b string) bool {
	normalize := func(s string) ([]string, error) {
		tokens, e := sqltext.ScanDialect(s, "sqlite")
		if e != nil {
			return nil, e
		}
		var out []string
		for _, t := range tokens {
			if t.Comment || t.Text == ";" {
				continue
			}
			text := t.Text
			if t.Quoted && strings.HasPrefix(text, "'") {
				out = append(out, text)
				continue
			}
			if t.Quoted {
				switch text[0] {
				case '"':
					text = strings.ReplaceAll(text[1:len(text)-1], "\"\"", "\"")
				case '`':
					text = strings.ReplaceAll(text[1:len(text)-1], "``", "`")
				case '[':
					text = text[1 : len(text)-1]
				}
			}
			out = append(out, strings.ToLower(text))
		}
		if len(out) >= 5 && slices.Equal(out[:5], []string{"create", "table", "if", "not", "exists"}) {
			out = append(out[:2], out[5:]...)
		}
		return out, nil
	}
	left, e := normalize(a)
	if e != nil {
		return false
	}
	right, e := normalize(b)
	return e == nil && slices.Equal(left, right)
}

func (s *session) ReadExistingHistory(ctx context.Context) ([]x.Record, error) {
	exists, err := s.inspect(ctx)
	if err != nil || !exists {
		return nil, err
	}
	return s.ReadHistory(ctx)
}
