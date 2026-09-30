package integration_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	x "github.com/sxwebdev/xmigrator"
	pgdriver "github.com/sxwebdev/xmigrator/driver/pgx"
)

func TestPGMetadataConstraints(t *testing.T) {
	pool := reviewPG(t)
	for _, tt := range []struct {
		name  string
		alter string
		want  error
	}{
		{name: "valid"},
		{name: "missing_not_null", alter: "ALTER COLUMN owner_id DROP NOT NULL", want: x.ErrMetadataConflict},
		{name: "extra_check", alter: "ADD CHECK (format_version = 1)", want: x.ErrMetadataConflict},
	} {
		t.Run(tt.name, func(t *testing.T) {
			schema := fmt.Sprintf("metadata_%d", time.Now().UnixNano())
			driver, err := pgdriver.FromPool(pool, pgdriver.Config{MetadataSchema: schema})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.WithoutCancel(t.Context()), 5*time.Second)
				defer cancel()
				if _, err := pool.Exec(ctx, "DROP SCHEMA IF EXISTS "+schema+" CASCADE"); err != nil {
					t.Error(err)
				}
			})
			bootstrap := func(session x.Session[pgx.Tx]) error { return session.EnsureMetadata(t.Context()) }
			if err := driver.WithSession(t.Context(), bootstrap); err != nil {
				t.Fatal(err)
			}
			if tt.alter != "" {
				if _, err := pool.Exec(t.Context(), "ALTER TABLE "+schema+".__xmigrator_meta "+tt.alter); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := driver.ReadHistorySnapshot(t.Context()); !errors.Is(err, tt.want) {
				t.Fatalf("metadata snapshot: %v, want %v", err, tt.want)
			}
			if err := driver.WithSession(t.Context(), bootstrap); !errors.Is(err, tt.want) {
				t.Fatalf("metadata reuse: %v, want %v", err, tt.want)
			}
		})
	}
}
