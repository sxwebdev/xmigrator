# Maintaining the library

## Repository layout

Keep five published Go modules independent:

- Root: dependency-free core.
- driver/pgx: native pgx v5 backend.
- driver/sqlite: database/sql backend without an engine, including test-only requirements; x/sys supports Windows locking.
- cli/urfavecli: urfave/cli v3 adapter without database drivers.
- cmd/xmigrator: executable composition, including modernc SQLite.

internal/integration is a sixth, unpublished module with engine dependencies and relative replacements so GOWORK=off still tests the local checkout. Published manifests use real module versions and no local replacements. Generate the ignored local go.work through `go run tools/dev.go workspace`. Root `go test ./...` does not traverse nested modules.

Keep code, README, skills, and documentation in English.

## Contracts to preserve

- Compute pending migrations by set difference, not MAX(version).
- Revert by descending ApplyOrder, not migration version.
- Snapshot source per operation, preflight the selected plan, and check history again inside each transaction.
- Hold the backend run lock across all steps; per-migration transactions do not prevent concurrent runs from interleaving.
- Keep metadata mutations with SQL/hooks; do not introduce a dirty flag as a substitute for transactional execution.
- Represent known commits even on cleanup failure, and expose unknown commit outcomes explicitly.
- Reject transaction escape before execution. A final active transaction status cannot detect COMMIT followed by BEGIN.
- Recognize PostgreSQL BEGIN ATOMIC bodies, SQLite trigger BEGIN/END, CASE END, and standalone SQLite END as distinct lexical cases.
- Preserve exclusive create plus version recheck. Same-version files with different names are different filesystem paths.
- Wait for OS locks through nonblocking attempts with context cancellation; blocking flock/LockFileEx cannot be cancelled by context alone.
- Keep metadata ownership/structure validation and reject unrelated objects instead of adopting them silently.

## Verification

Use existing repository tools when working in this checkout:

```sh
go run tools/dev.go workspace
make test
```

For full checks, set `XMIGRATOR_TEST_PG_DSN` to a disposable PostgreSQL database and run `make check`. This explicitly visits all six modules, runs vet/race and real PostgreSQL/SQLite integration tests, merges cross-module and executable coverage, and requires 100% statements per production package. Test-only contract assertions are excluded from the production gate.

For backend changes, verify behavior against real databases: rolled-back DDL/data/history, run lock serialization, cancellation, metadata conflicts, actual reverse ordering, late migrations, and drop dependency handling. A successful mock SQL call does not prove these effects.

Use `make sqlite-check` for the SQLite/executable matrix without PostgreSQL. Run `go run tools/dev.go verify-install` for release manifest/install changes; it uses a local proxy and does not publish. Run `go run tools/dev.go prove-regressions` when evaluating the regression suite; it temporarily mutates/restores sources, so avoid concurrent source editing.

Select verification proportional to the change. Do not rerun destructive integration workflows for prose-only changes. Report exactly which checks ran and distinguish build portability from runtime testing on the target OS.
