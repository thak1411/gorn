# MySQL migrations

The migration engine inspects MySQL and changes only model-owned definitions
that differ. A repeat run produces zero DDL. MySQL **8.0.40 and 8.4.7** are the
integration-test targets; other engines and MySQL major versions are rejected.
Advanced MySQL features which cannot be represented faithfully are preserved or
rejected with an error, rather than silently rewritten.

## Preview and apply

```go
tables := []gorn.MigrationTable{
    {Name: "users", Model: &User{}},
    {Name: "posts", Model: &Post{}},
}

// SELECT and SHOW only. This does not create a history table, set session
// variables, acquire a migration lock, or execute DDL/DML.
plan, err := db.PlanMigration(ctx, tables, indexes)
if err != nil {
    return err
}
fmt.Print(plan.SQL())
for _, step := range plan.Steps {
    fmt.Printf("%s: %s (destructive=%v)\n", step.Table, step.Reason, step.Destructive)
}
for _, warning := range plan.Warnings {
    fmt.Println(warning)
}

// Call only when the inspected plan should actually be executed.
return db.ApplyMigration(ctx, plan)
```

`json.MarshalIndent(plan, "", "  ")` exports the preview for logs/UI. Apply the
original, unmodified in-memory plan. Edited or deserialized plans are rejected.
The plan records a fingerprint of the server identity and schema, and applying
it after schema drift fails with `ErrMigrationDrift`. Ordinary INSERT/UPDATE/
DELETE operations, including an advancing AUTO_INCREMENT counter, do not count
as schema drift. The preview describes DDL, not a database execution-cost
estimate or proof that current/future rows satisfy new constraints.

For an automatically applied migration:

```go
plan, err := db.Migrate(ctx, tables, indexes)
```

All inputs are validated before execution. Include **all new related tables in
one call**: tables and indexes are created first, existing table changes are
applied next, then foreign keys are installed. This supports forward references,
self references and cycles without disabling foreign-key checks. Existing
index replacements use one `ALTER TABLE ... DROP INDEX ..., ADD INDEX ...`
statement, so a duplicate-data failure preserves the old index.

## Models and defaults

```go
type Post struct {
    ID     int64  `rnsql:"id" rntype:"BIGINT" rnopt:"PK AI"`
    UserID int64  `rnsql:"user_id" rntype:"BIGINT" rnopt:"NN" FK:"users.id"`
    Title  string `rnsql:"title" rntype:"VARCHAR(200)" rnopt:"NN" rndefault:"'Untitled'"`
    Active bool   `rnsql:"active" rntype:"BOOLEAN" rnopt:"NN" rndefault:"TRUE"`
}
```

- `PK`, `NN`, `UN`, `AI`, `BIN`, and `UQ` are accepted options. Unknown options,
  duplicate names and malformed models are errors rather than panics.
- Integer display widths and aliases such as `INTEGER`/`INT`, `BOOLEAN`/`TINYINT`
  are normalized. Decimal precision, unsignedness, enum/set member order and
  temporal precision are significant.
- `UQ` now enforces single-column uniqueness. An equivalent primary/unique index
  is reused; composite/prefix indexes do not imply single-column uniqueness.
- Omitted `rndefault` preserves an existing default. It does not use a Go zero
  value as a backfill. For new columns it specifies no default. Supported explicit
  defaults are SQL string/numeric literals, NULL, TRUE/FALSE and
  CURRENT_TIMESTAMP with optional precision. Expressions beyond this subset
  require explicit SQL. Literal escaping follows the inspected session sql_mode.
  Character columns require a quoted string or NULL, avoiding ambiguous numeric
  coercions (for example, `'001'` is distinct from `'1'`).
- Existing defaults, comments, character sets, collations, ON UPDATE clauses and
  unmodeled columns are preserved when widening a supported column. Reordering
  fields does not reorder physical columns. New columns are appended.
- `FKOnDelete`/`FKOnUpdate` optionally specify RESTRICT, NO ACTION, CASCADE or
  SET NULL. Omitted actions preserve an equivalent existing constraint's rules
  and name. RESTRICT/NO ACTION compare equivalently. SET NULL requires a nullable
  column. Existing composite foreign keys are preserved; new composite FKs need
  explicit SQL because a field's FK tag represents one column.

## Explicit permissions and online-DDL policy

```go
plan, err := db.PlanMigration(ctx, tables, indexes, gorn.MigrationOptions{
    Algorithm:       "INPLACE",
    Lock:            "NONE",
    LockWaitTimeout: 5 * time.Second,
})
```

`Algorithm` accepts DEFAULT/INSTANT/INPLACE/COPY. `Lock` accepts DEFAULT/NONE/
SHARED/EXCLUSIVE. INSTANT accepts only DEFAULT locking. Empty options let MySQL
choose. Unsupported operations fail instead of silently retrying with a slower
algorithm or stronger lock. This is not a guarantee of zero lock time: MySQL
still uses metadata locks. The context and metadata-lock timeout bound waiting;
actual execution time depends on the table, data, server and workload.

By default, omitted columns, indexes, primary keys and foreign keys are kept.
Only explicitly supplied tables are eligible for changes. No mode automatically
drops tables. `MigrationOptions` provides separate opt-ins:

| Option | Meaning |
| --- | --- |
| `AllowDropColumns` | Drop unmodeled, unindexed, unreferenced columns of supplied table models. |
| `AllowDropIndexes` | Drop omitted ordinary secondary indexes of supplied tables. Gorn FK backing indexes are kept. |
| `AllowDropForeignKeys` | Drop omitted foreign keys of supplied table models; keep backing indexes. |
| `AllowUnsafeChanges` | Permit narrowing/type conversion, NOT NULL tightening, auto-increment/collation changes, replacing existing PK/UNIQUE/FK constraints, or NOT NULL additions without explicit defaults. |

The preview labels destructive/unsafe operations. Permissions do **not** disable
MySQL integrity checks or permit truncation: execution forces strict SQL mode
and foreign_key_checks=1. A narrowing conversion with oversized data or NOT NULL
conversion with NULLs fails. Backfill data explicitly before retrying. Renames,
indexed/referenced column removal, cross-table FK type changes, generated-column
changes, functional/invisible/fulltext/spatial index changes and arbitrary
expressions require an explicit staged migration. Do not infer a rename from
one missing and one new field; the engine preserves the old column by default.

## Execution and failures

- One pinned `sql.Conn` executes the plan, with a schema-wide `GET_LOCK` named
  `gorn:migrate:` followed by the first 48 hex characters of SHA-256(schema).
  A second cooperating gorn migrator gets `ErrMigrationBusy`. External schema
  tools must coordinate using the same lock; it does not block application DML.
- The schema fingerprint is rechecked while holding the migration lock. Planning
  also detects changes during its multi-query inspection. It is not possible to
  prevent uncooperative external DDL with an advisory lock.
- Session SQL mode, FK checks and metadata-lock timeout are restored on success
  or error. Cleanup uses a separate bounded context if the caller was canceled.
  Failed cleanup discards the connection rather than returning a dirty session
  to the pool. Transactions are rejected: MySQL DDL implicitly commits.
- Atomicity is **per DDL statement, not across the entire plan**. A
  `MigrationApplyError` carries the failed zero-based step, SQL, wrapped cause,
  and number of acknowledged successes. Earlier successful statements remain.
  A connection loss can leave the failed statement's outcome unknown. Inspect
  the DB and generate a new plan; do not blindly replay the original SQL.
- Re-planning after partial success includes only work still outstanding. Errors
  are returned to callers. Applications must propagate them to nonzero process
  exit status; gorn cannot fix an application that logs and ignores an error.
- Backups, verified restoration, production-sized rehearsal, data backfills and
  application-version compatibility remain deployment responsibilities. Passing
  tests is not a 100% guarantee for every production schema or failure mode.

## Legacy APIs

`Migration`, `AlterTable` and `MigrationIndex` now route through the conservative
engine. `MigrationIndex` no longer deletes all indexes absent from its input.
Low-level explicit operations (`DropColumn`, `DropIndex`, `DropForeignKey`,
`CreateTable`, etc.) are still explicit DDL primitives; they are not a previewed
multi-table migration. `DropForeignKey` no longer also drops a same-named index.
`Sql.Default` now emits a literal default rather than doing nothing.
`GetIndexes` fixes cross-table name grouping and rejects index features its
public struct cannot represent. Metadata lookups use equality rather than LIKE;
row-stream errors propagate instead of appearing as an empty schema.

## Local disposable validation

```sh
# Unit tests; real-DB tests explicitly skip without a disposable DB address.
go test ./...

# Prepare a disposable local MySQL server first, then supply its loopback port.
GORN_MYSQL_TEST_ADDR=127.0.0.1:3307 go test -race -count=1 -timeout=3m ./...

# Short parser fuzz run (no database).
go test -run '^$' -fuzz FuzzMigrationSQLFragments -fuzztime=15s -parallel=4 .
```

Tests create/drop only unique `gorn_test_*` schemas and never read application
configuration or credentials. Start and stop the disposable MySQL server yourself;
there is no container-management script or hosted test workflow in this repository.
`GORN_MYSQL_TEST_ADDR` requires a numeric loopback address and root access to create
isolated databases. Set `GORN_MYSQL_TEST_PASSWORD` if the disposable server has a
root password. Never forward the address to a production server. The tests use
real MySQL DDL and rows plus driver fault injection for paths that cannot be made
deterministic through server input.

Coverage includes no-op and minimal-change DDL counts; populated table additions
and widening; defaults, comments, Unicode, charset/collation, timestamp behavior;
PK ordering/auto increment; index identity, prefix/direction and failed unique
replacement; FK order/cycles/actions/composites/orphans; destructive gates;
schema drift, modified plans, wrong targets; concurrent migration locks; metadata
lock timeout/cancellation; session restoration and failed cleanup; row-stream and
metadata read failures; injected failure at every step and re-planning; unsupported
objects; duplicate index definitions; and an independently generated multi-table
schema with a minimal-change check.

MySQL references: [atomic DDL](https://dev.mysql.com/doc/refman/8.0/en/atomic-ddl.html),
[ALTER TABLE attribute preservation](https://dev.mysql.com/doc/refman/8.0/en/alter-table.html),
[online DDL limitations](https://dev.mysql.com/doc/refman/8.0/en/innodb-online-ddl-limitations.html),
[advisory locks](https://dev.mysql.com/doc/refman/8.0/en/locking-functions.html).
