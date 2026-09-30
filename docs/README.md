# xmigrator documentation

xmigrator runs SQL migrations in Go applications and through a standalone CLI. It supports PostgreSQL 14+ through pgx v5 and SQLite 3.37+ through `database/sql`. Go 1.27+ is required.

- [Library usage](usage.md): installation, sources, operations, policies, and logging.
- [Migration files and hooks](migrations.md): filenames, directives, transactions, and Go callbacks.
- [Drivers](drivers.md): connections, locking, timeouts, and drop configuration.
- [CLI](cli.md): the standalone command and urfave/cli v3 integration.
- [History and operations](operations.md): metadata tables, diagnostics, repair, and switching from another library.
- [Development and releases](release.md): module checks, installation, and version publishing.

See the [project README](../README.md) for a quick example. Licensed under [MIT](../LICENSE).
