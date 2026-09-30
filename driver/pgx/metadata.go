package pgx

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	x "github.com/sxwebdev/xmigrator"
	"github.com/sxwebdev/xmigrator/internal/metadata"
)

type queryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func (d *Driver) inspect(ctx context.Context, q queryer) (bool, error) {
	found := 0
	for _, suffix := range []string{"meta", "history"} {
		var kind string
		var oid uint32
		e := q.QueryRow(ctx, "SELECT c.relkind::text,c.oid FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=$1 AND c.relname=$2", d.cfg.MetadataSchema, d.prefix+suffix).Scan(&kind, &oid)
		if errors.Is(e, pgx.ErrNoRows) {
			continue
		}
		if e != nil {
			return false, e
		}
		found++
		if kind != "r" {
			return false, x.ErrMetadataConflict
		}
		rows, e := q.Query(ctx, "SELECT a.attname,pg_catalog.format_type(a.atttypid,a.atttypmod),a.attnotnull,a.attidentity::text FROM pg_catalog.pg_attribute a WHERE a.attrelid=$1 AND a.attnum>0 AND NOT a.attisdropped ORDER BY a.attnum", oid)
		if e != nil {
			return false, e
		}
		var columns []string
		for rows.Next() {
			var name, typ, identity string
			var required bool
			if e = rows.Scan(&name, &typ, &required, &identity); e != nil {
				break
			}
			columns = append(columns, fmt.Sprintf("%s:%s:%t:%s", name, typ, required, identity))
		}
		rows.Close()
		e = errors.Join(e, rows.Err())
		if e != nil {
			return false, e
		}
		expected := []string{"id:integer:true:", "owner_id:text:true:", "format_version:integer:true:"}
		if suffix == "history" {
			expected = []string{"version:bigint:true:", "name:text:true:", "up_checksum:bytea:true:", "down_checksum:bytea:true:", "down_kind:text:true:", "up_hook_revision:text:true:", "down_hook_revision:text:true:", "applied_at:timestamp with time zone:true:", "apply_order:bigint:true:a"}
		}
		if !slices.Equal(columns, expected) {
			return false, x.ErrMetadataConflict
		}
		// PostgreSQL 18 also stores table NOT NULL constraints here; attnotnull above already verifies them.
		rows, e = q.Query(ctx, "SELECT pg_catalog.pg_get_constraintdef(oid) FROM pg_catalog.pg_constraint WHERE conrelid=$1 AND contype <> 'n' ORDER BY 1", oid)
		if e != nil {
			return false, e
		}
		var constraints []string
		for rows.Next() {
			var value string
			if e = rows.Scan(&value); e != nil {
				break
			}
			constraints = append(constraints, value)
		}
		rows.Close()
		e = errors.Join(e, rows.Err())
		if e != nil {
			return false, e
		}
		expectedConstraints := []string{"CHECK ((id = 1))", "PRIMARY KEY (id)"}
		if suffix == "history" {
			expectedConstraints = []string{"CHECK ((version > 0))", "CHECK ((octet_length(up_checksum) = 32))", "CHECK ((octet_length(down_checksum) = 32))", "CHECK ((down_kind = ANY (ARRAY['sql'::text, 'noop'::text, 'irreversible'::text])))", "PRIMARY KEY (version)", "UNIQUE (apply_order)"}
		}
		slices.Sort(expectedConstraints)
		if !slices.Equal(constraints, expectedConstraints) {
			return false, x.ErrMetadataConflict
		}
		var altered bool
		if e = q.QueryRow(ctx, "SELECT c.relrowsecurity OR EXISTS(SELECT 1 FROM pg_catalog.pg_trigger WHERE tgrelid=c.oid AND NOT tgisinternal) OR EXISTS(SELECT 1 FROM pg_catalog.pg_rewrite WHERE ev_class=c.oid) FROM pg_catalog.pg_class c WHERE c.oid=$1", oid).Scan(&altered); e != nil {
			return false, e
		}
		if altered {
			return false, x.ErrMetadataConflict
		}
	}
	if found == 0 {
		return false, nil
	}
	if found != 2 {
		return false, x.ErrMetadataConflict
	}
	var count int
	if e := q.QueryRow(ctx, "SELECT count(*) FROM "+d.table("meta")).Scan(&count); e != nil {
		return false, e
	}
	if count != 1 {
		return false, x.ErrMetadataConflict
	}
	var id, format int
	var owner string
	if e := q.QueryRow(ctx, "SELECT id,owner_id,format_version FROM "+d.table("meta")).Scan(&id, &owner, &format); e != nil {
		return false, e
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
	_, e := s.InTx(ctx, x.Script{Kind: x.DownNoop}, func(t x.Transaction[pgx.Tx]) error {
		q := t.Executor()
		exists, e := s.d.inspect(ctx, q)
		if e != nil || exists {
			return e
		}
		if s.d.cfg.RequireExistingMetadata {
			return x.ErrMetadataConflict
		}
		var schemaExists bool
		if e = q.QueryRow(ctx, "SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_namespace WHERE nspname=$1)", s.d.cfg.MetadataSchema).Scan(&schemaExists); e != nil {
			return e
		}
		if !schemaExists {
			if _, e = q.Exec(ctx, "CREATE SCHEMA IF NOT EXISTS "+ident(s.d.cfg.MetadataSchema)); e != nil {
				return e
			}
		}
		statements := []string{
			"CREATE TABLE " + s.d.table("meta") + " (id INTEGER PRIMARY KEY CHECK(id=1), owner_id TEXT NOT NULL, format_version INTEGER NOT NULL)",
			"CREATE TABLE " + s.d.table("history") + " (version BIGINT PRIMARY KEY CHECK(version>0), name TEXT NOT NULL, up_checksum BYTEA NOT NULL CHECK(octet_length(up_checksum)=32), down_checksum BYTEA NOT NULL CHECK(octet_length(down_checksum)=32), down_kind TEXT NOT NULL CHECK(down_kind IN ('sql','noop','irreversible')), up_hook_revision TEXT NOT NULL, down_hook_revision TEXT NOT NULL, applied_at TIMESTAMPTZ NOT NULL, apply_order BIGINT GENERATED ALWAYS AS IDENTITY UNIQUE)",
		}
		for _, statement := range statements {
			if _, e = q.Exec(ctx, statement); e != nil {
				return e
			}
		}
		_, e = q.Exec(ctx, "INSERT INTO "+s.d.table("meta")+" VALUES(1,$1,$2)", metadata.Owner, metadata.Format)
		return e
	})
	return e
}

func (d *Driver) readHistory(ctx context.Context, q queryer) (out []x.Record, err error) {
	rows, e := q.Query(ctx, "SELECT version,name,up_checksum,down_checksum,down_kind,up_hook_revision,down_hook_revision,applied_at,apply_order FROM "+d.table("history")+" ORDER BY apply_order")
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var r x.Record
		var up, down []byte
		if e = rows.Scan(&r.Version, &r.Name, &up, &down, &r.DownKind, &r.UpHookRevision, &r.DownHookRevision, &r.AppliedAt, &r.ApplyOrder); e != nil {
			return nil, e
		}
		if len(up) != 32 || len(down) != 32 {
			return nil, x.ErrMetadataConflict
		}
		copy(r.UpChecksum[:], up)
		copy(r.DownChecksum[:], down)
		out = append(out, r)
	}
	return out, rows.Err()
}

func (s *session) Drop(ctx context.Context) error {
	if len(s.d.cfg.DropSchemas) == 0 {
		return x.ErrDropScope
	}
	outcome, e := s.InTx(ctx, x.Script{Kind: x.DownNoop}, func(t x.Transaction[pgx.Tx]) error {
		q := t.Executor()
		var extension bool
		e := q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_depend d JOIN pg_catalog.pg_class c ON d.classid='pg_class'::regclass AND d.objid=c.oid JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE d.deptype='e' AND n.nspname=ANY($1)) OR EXISTS(SELECT 1 FROM pg_catalog.pg_depend d JOIN pg_catalog.pg_proc p ON d.classid='pg_proc'::regclass AND d.objid=p.oid JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace WHERE d.deptype='e' AND n.nspname=ANY($1)) OR EXISTS(SELECT 1 FROM pg_catalog.pg_depend d JOIN pg_catalog.pg_type t ON d.classid='pg_type'::regclass AND d.objid=t.oid JOIN pg_catalog.pg_namespace n ON n.oid=t.typnamespace WHERE d.deptype='e' AND n.nspname=ANY($1))`, s.d.cfg.DropSchemas).Scan(&extension)
		if e != nil {
			return e
		}
		if extension {
			return fmt.Errorf("%w: extension-owned objects", x.ErrDropScope)
		}
		var unsupported bool
		e = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_catalog.pg_operator WHERE oprnamespace IN (SELECT oid FROM pg_catalog.pg_namespace WHERE nspname=ANY($1))) OR EXISTS(SELECT 1 FROM pg_catalog.pg_opclass WHERE opcnamespace IN (SELECT oid FROM pg_catalog.pg_namespace WHERE nspname=ANY($1))) OR EXISTS(SELECT 1 FROM pg_catalog.pg_opfamily WHERE opfnamespace IN (SELECT oid FROM pg_catalog.pg_namespace WHERE nspname=ANY($1))) OR EXISTS(SELECT 1 FROM pg_catalog.pg_collation WHERE collnamespace IN (SELECT oid FROM pg_catalog.pg_namespace WHERE nspname=ANY($1))) OR EXISTS(SELECT 1 FROM pg_catalog.pg_conversion WHERE connamespace IN (SELECT oid FROM pg_catalog.pg_namespace WHERE nspname=ANY($1))) OR EXISTS(SELECT 1 FROM pg_catalog.pg_ts_config WHERE cfgnamespace IN (SELECT oid FROM pg_catalog.pg_namespace WHERE nspname=ANY($1))) OR EXISTS(SELECT 1 FROM pg_catalog.pg_ts_dict WHERE dictnamespace IN (SELECT oid FROM pg_catalog.pg_namespace WHERE nspname=ANY($1))) OR EXISTS(SELECT 1 FROM pg_catalog.pg_ts_parser WHERE prsnamespace IN (SELECT oid FROM pg_catalog.pg_namespace WHERE nspname=ANY($1))) OR EXISTS(SELECT 1 FROM pg_catalog.pg_ts_template WHERE tmplnamespace IN (SELECT oid FROM pg_catalog.pg_namespace WHERE nspname=ANY($1))) OR EXISTS(SELECT 1 FROM pg_catalog.pg_statistic_ext WHERE stxnamespace IN (SELECT oid FROM pg_catalog.pg_namespace WHERE nspname=ANY($1)))`, s.d.cfg.DropSchemas).Scan(&unsupported)
		if e != nil {
			return e
		}
		if unsupported {
			return fmt.Errorf("%w: unsupported object class in selected schema", x.ErrDropScope)
		}
		rows, e := q.Query(ctx, `SELECT c.relkind::text,quote_ident(n.nspname)||'.'||quote_ident(c.relname) FROM pg_catalog.pg_class c JOIN pg_catalog.pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname=ANY($1) AND c.relkind IN ('r','p','f','v','m','S') UNION ALL SELECT CASE p.prokind WHEN 'p' THEN 'procedure' WHEN 'a' THEN 'aggregate' ELSE 'function' END,quote_ident(n.nspname)||'.'||quote_ident(p.proname)||'('||pg_catalog.pg_get_function_identity_arguments(p.oid)||')' FROM pg_catalog.pg_proc p JOIN pg_catalog.pg_namespace n ON n.oid=p.pronamespace WHERE n.nspname=ANY($1) AND p.prokind IN ('p','f','w','a') UNION ALL SELECT CASE t.typtype WHEN 'd' THEN 'domain' ELSE 'type' END,quote_ident(n.nspname)||'.'||quote_ident(t.typname) FROM pg_catalog.pg_type t JOIN pg_catalog.pg_namespace n ON n.oid=t.typnamespace LEFT JOIN pg_catalog.pg_class c ON c.oid=t.typrelid WHERE n.nspname=ANY($1) AND (t.typtype IN ('e','d','r') OR t.typtype='b' AND t.typelem=0 OR t.typtype='c' AND c.relkind='c')`, s.d.cfg.DropSchemas)
		if e != nil {
			return e
		}
		var statements []string
		var tables []string
		for rows.Next() {
			var kind, name string
			if e = rows.Scan(&kind, &name); e != nil {
				break
			}
			switch kind {
			case "r", "p":
				tables = append(tables, name)
			case "f":
				statements = append(statements, "DROP FOREIGN TABLE IF EXISTS "+name+" RESTRICT")
			case "v":
				statements = append(statements, "DROP VIEW IF EXISTS "+name+" RESTRICT")
			case "m":
				statements = append(statements, "DROP MATERIALIZED VIEW IF EXISTS "+name+" RESTRICT")
			case "S":
				statements = append(statements, "DROP SEQUENCE IF EXISTS "+name+" RESTRICT")
			default:
				statements = append(statements, "DROP "+strings.ToUpper(kind)+" IF EXISTS "+name+" RESTRICT")
			}
		}
		rows.Close()
		e = errors.Join(e, rows.Err())
		if e != nil {
			return e
		}
		if len(tables) > 0 {
			statements = append(statements, "DROP TABLE IF EXISTS "+strings.Join(tables, ",")+" RESTRICT")
		}
		// RESTRICT plus savepoints permits internal dependency ordering without deleting external objects.
		for len(statements) > 0 {
			var next []string
			var last error
			for _, stmt := range statements {
				if _, e = q.Exec(ctx, "SAVEPOINT xmigrator_drop"); e != nil {
					return e
				}
				_, e = q.Exec(ctx, stmt)
				if e != nil {
					last = e
					if _, r := q.Exec(ctx, "ROLLBACK TO SAVEPOINT xmigrator_drop"); r != nil {
						return errors.Join(e, r)
					}
					next = append(next, stmt)
				}
				if _, e = q.Exec(ctx, "RELEASE SAVEPOINT xmigrator_drop"); e != nil {
					return e
				}
			}
			if len(next) == len(statements) {
				return errors.Join(x.ErrDropScope, last)
			}
			statements = next
		}
		_, e = q.Exec(ctx, "DELETE FROM "+s.d.table("history"))
		return e
	})
	if outcome == x.TxUnknown {
		return errors.Join(x.ErrCommitOutcomeUnknown, e)
	}
	return e
}
