package app

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5/pgconn"
	x "github.com/sxwebdev/xmigrator"
)

// ErrorMessage provides a safe process-level diagnostic. Run still returns the full
// error to its caller; backend messages can contain credentials or application SQL.
func ErrorMessage(err error) string {
	if migration, ok := errors.AsType[*x.MigrationError](err); ok {
		return fmt.Sprintf("%s migration %d (%s), %s: %s", migration.Direction, migration.Version, migration.Name, migration.Stage, ErrorMessage(migration.Err))
	}
	if source, ok := errors.AsType[*x.SourceError](err); ok {
		return fmt.Sprintf("%s: %s", source.File, ErrorMessage(source.Err))
	}
	for _, entry := range []struct {
		cause error
		text  string
	}{
		{x.ErrCommitOutcomeUnknown, "commit outcome unknown; inspect history before retrying"},
		{x.ErrChecksumMismatch, "migration checksum mismatch"},
		{x.ErrDefinitionMismatch, "migration definition mismatch"},
		{x.ErrHookMismatch, "migration hooks or revisions do not match"},
		{x.ErrIrreversible, "selected migration is irreversible"},
		{x.ErrUnknownApplied, "selected history contains unknown migrations"},
		{x.ErrMetadataConflict, "metadata ownership or structure conflict"},
		{x.ErrMetadataVersion, "unsupported metadata format"},
		{x.ErrHistoryConflict, "migration history changed unexpectedly"},
		{x.ErrInvalidSource, "invalid SQL migration source"},
		{x.ErrInvalidSteps, "invalid steps"},
		{x.ErrVersionExists, "migration version already exists"},
		{x.ErrInvalidConfig, "invalid configuration; see command help"},
		{x.ErrNotApplied, "migration is not applied"},
		{x.ErrUnsafeSQL, "script or connection settings violate transaction ownership"},
		{x.ErrDropScope, "drop scope is invalid or has external dependencies"},
		{x.ErrForeignKey, "migration violates foreign keys"},
		{context.Canceled, "operation canceled"},
		{context.DeadlineExceeded, "operation timed out"},
	} {
		if errors.Is(err, entry.cause) {
			return entry.text
		}
	}
	if pgerr, ok := errors.AsType[*pgconn.PgError](err); ok {
		return fmt.Sprintf("database rejected operation (SQLSTATE %s)", pgerr.Code)
	}
	if code, ok := errors.AsType[interface {
		error
		Code() int
	}](err); ok {
		reason := "database rejected operation"
		for _, entry := range []struct{ pattern, reason string }{
			{"no such table:", "table does not exist"},
			{"no such column:", "column does not exist"},
			{"syntax error", "SQL syntax error"},
			{"already exists", "database object already exists"},
			{"UNIQUE constraint failed", "unique constraint violation"},
			{"NOT NULL constraint failed", "not-null constraint violation"},
			{"FOREIGN KEY constraint failed", "foreign-key constraint violation"},
			{"CHECK constraint failed", "check constraint violation"},
		} {
			if strings.Contains(code.Error(), entry.pattern) {
				reason = entry.reason
				break
			}
		}
		return fmt.Sprintf("%s (SQLite code %d)", reason, code.Code())
	}
	return "command failed"
}
