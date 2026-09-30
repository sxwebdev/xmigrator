---
name: xmigrator
description: Integrate and maintain the github.com/sxwebdev/xmigrator Go SQL migration library. Use this skill whenever working with xmigrator, its .up.sql/.down.sql files, pgx v5 or SQLite drivers, typed migration hooks and revisions, checksum policies, backfill, repair-down, migration locking, standalone xmigrator commands, or the cli/urfavecli adapter. Also use it when replacing copied migration runners with xmigrator or changing this repository's driver and runner contracts.
user-invocable: true
---

# xmigrator

## Overview

Build transactional SQL migration workflows with a dependency-free core and separately imported drivers and CLI adapters. Preserve the distinction between migration version and actual application order: late files are valid pending migrations, and rollback reverses application order.

## Instructions

1. Identify the requested workflow: application integration, migration authoring, CLI integration, history diagnosis, or library maintenance. Inspect the installed API or checkout before editing because examples describe the current implementation, not a promise of compatibility with every release.
2. Select only the required modules. Keep pgx, SQLite engines, and CLI frameworks out of the core. Accept the application's existing pool or DB rather than replacing its connection lifecycle.
3. Read the relevant bundled reference:
   - [Core and migration files](references/core.md): sources, operations, directives, checksums, repair, and logging.
   - [Drivers](references/drivers.md): PostgreSQL and SQLite construction, locking, ownership, and drop scope.
   - [Hooks](references/hooks.md): typed callbacks, revision matching, transaction boundaries, and a complete example.
   - [CLI](references/cli.md): standalone commands and urfave/cli v3 integration.
   - [Maintenance](references/maintenance.md): module boundaries and meaningful verification in this repository.
4. Create matching up/down files with unique positive versions and valid SQL or an explicit supported directive. Preserve existing applied definitions; prefer a new migration for new behavior. Backfill support does not make dependent SQL order-independent.
5. Validate the source and test the requested behavior against the chosen backend. File validation does not validate runtime hook registration or replace backend SQL checks. Keep context cancellation and errors visible to the caller.
6. For failures, inspect returned actions and history before retrying. Earlier migration commits survive a later failure, and an unknown commit outcome requires reconciliation rather than blind replay.
7. For an existing database from another migrator, explain the history mismatch before applying anything. There is no automatic history import or baseline feature; changing a prefix does not transfer applied state.

## Examples

**Input:** "Replace our copied PostgreSQL migration runner with xmigrator; keep our pgxpool and embedded SQL."

**Output:** Create a source using `NewSource(embeddedFiles, "migrations")`, a driver using `pgdriver.FromPool(pool, pgdriver.Config{})`, and a runner using `xmigrator.New(source, driver)`. Call `Up(ctx, 0)` during startup and return its error. Preserve ownership of the pool. Check whether the existing files and history need a separate transition before running against an already migrated database.

**Input:** "Add Go logic to populate data after migration 100."

**Output:** Add `-- xmigrator:hooks=seed-1` to the up header and register version `100` with `UpRevision: "seed-1"` and an `AfterUp` callback. Execute writes through the supplied transaction. Define down behavior explicitly; do not add a down hook revision unless down callbacks are registered.

## Output format

For implementation tasks, produce the smallest working change: migration pairs, driver/runner wiring, and the relevant verification. Report the selected modules, operation semantics, and any unresolved transition of existing history. For explanations, use concrete API or command examples; do not generate configuration or run database operations unnecessarily.

## Key principles

- Keep SQL, hooks, and history in the same per-migration transaction so a failed callback cannot leave its migration marked applied.
- Keep the run lock around the entire up/down/drop/repair operation so concurrent plans cannot interleave steps.
- Treat hook revisions as explicit developer-maintained identifiers; SQL checksums cannot detect changes to Go function bodies.
- Use repair only to accept the current down definition, never to pretend an up migration ran.
- Preserve the caller's requested backend and dependency choices. MariaDB and ClickHouse are roadmap items, not available drivers.
