# 0118-0047 — alter-table-1: VALIDATE CONSTRAINT lock semantics

**Milestone:** M0118-0008 (PostgreSQL isolation-spec output parity, D-002)
**Status:** landed — `alter-table-1` promoted to pass-required
**Spec:** `postgres/src/test/isolation/specs/alter-table-1.spec`
**Test:** `TestPort_IsolationAlterTable1` (`internal/testport/isolation_port_test.go`)

## Problem

The `alter-table-1` isolation spec is the fifteenth promotion in the
DDL/VACUUM/maintenance-concurrency group. It mixes, all in session `s1`:

```
at1  ALTER TABLE b ADD CONSTRAINT bfk FOREIGN KEY (a_id) REFERENCES a (i) NOT VALID;
at2  ALTER TABLE b VALIDATE CONSTRAINT bfk;
```

with a second session `s2` that runs `SELECT … LIMIT` reads, an
`INSERT INTO b VALUES (0)`, and a `COMMIT`, across 170 permutations.

The spec's own comment states the design intent:

> VALIDATE allows a minimum of ShareUpdateExclusiveLock so we mix reads with it
> to see what works or waits.

The `ADD CONSTRAINT … NOT VALID` half already parses and locks correctly as of
design 0118-0046 (`alter-table-2`). The only missing piece was
`ALTER TABLE … VALIDATE CONSTRAINT name`, which goopg did not parse.

## Lock analysis

`AlterTableGetLockLevel` (PostgreSQL `tablecmds.c`) maps:

| action | lock level |
|---|---|
| `AT_AddConstraint` (ADD FK) | `ShareRowExclusiveLock` |
| `AT_ValidateConstraint` (VALIDATE) | `ShareUpdateExclusiveLock` |

`ShareUpdateExclusiveLock` conflicts only with itself-or-stronger
(`ShareUpdateExclusive`, `Share`, `ShareRowExclusive`, `Exclusive`,
`AccessExclusive`). It does **not** conflict with `AccessShareLock` (SELECT),
`RowShareLock` (FOR UPDATE), or `RowExclusiveLock` (INSERT/UPDATE/DELETE).

Since `s2` only ever takes `AccessShare` (reads) and `RowExclusive` (the
`INSERT`), `at2` never blocks `s2` and `s2` never blocks `at2`. The **only**
blocking the spec exhibits is the `INSERT` (`RowExclusiveLock`) waiting on the
still-uncommitted `ADD CONSTRAINT`'s `ShareRowExclusiveLock` — identical to
`alter-table-2`. Reads proceed throughout (`ShareRowExclusive` is compatible
with `AccessShare`).

So no new conflict matrix entry is needed: VALIDATE just had to parse and take
the (non-conflicting-with-`s2`) `ShareUpdateExclusiveLock` to be held to COMMIT,
matching PG's lock lifecycle.

## Change

1. **Parser** (`internal/parser/ast.go`, `internal/parser/ddl.go`): new
   `AlterTableValidateConstraint` action kind. `parseAlterTableAction` matches
   `VALIDATE CONSTRAINT name` (`VALIDATE` is not a reserved keyword, so it is
   matched as an identifier-keyword) and records the constraint name.

2. **Executor** (`internal/executor/operators_ddl.go`, `AlterTable` dispatch):
   the new case takes a transaction-scoped `ShareUpdateExclusiveLock` via
   `acquireDDLLockTxn` (no-op in autocommit / for system catalogs, keeping the
   pg_dump-restore / pgbench path lock-free) and flips the named FK's
   `convalidated` flag from `'f'` to `'t'` (`ForeignKey.NotValid = false`). An
   unknown constraint name raises `42704`, matching PostgreSQL.

No engine change beyond parse + lock + flag; VALIDATE produces no result rows,
so the spec's output parity rides entirely on the wait/no-wait timing.

## Verification

- `TestPort_IsolationAlterTable1` — strict (`runIsoSpecStrict`) PASS, all
  permutations byte-identical to PG 18.3.
- Sibling `TestPort_IsolationAlterTable2` / `AlterTable3` — strict PASS.
- `internal/parser/...` and `internal/executor/...` unit tests PASS; `go vet`
  clean.
- pgbench TPC-B smoke via the mandatory pre-commit hook (DDL parse-only change,
  autocommit no-op preserves the dump/load lock-free path).

## Oracle

`postgres/src/backend/commands/tablecmds.c` (`AlterTableGetLockLevel`,
`AT_ValidateConstraint`).
