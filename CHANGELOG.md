# Changelog

## v0.1.0 — 2026-10-01

Initial release:

- Dependency-free Go core with separately installed PostgreSQL, SQLite, and CLI modules.
- PostgreSQL support through native pgx v5 and SQLite through an application-owned database/sql connection pool.
- Transactional up/down migrations with step limits, run locking, and support for late migration versions.
- Typed transaction hooks with explicit revisions, optional checksum verification, and pluggable logging.
- Migration file creation, source validation, read-only status, scoped drop, and explicit down metadata repair.
- urfave/cli v3 adapter and installable xmigrator command with PostgreSQL and SQLite engines.
