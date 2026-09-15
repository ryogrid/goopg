# M0142-0003h — DDL-inside-`BEGIN` visibility/rollback repro: root cause pinned

| Field       | Value                          |
| ----------- | ------------------------------ |
| Status      | accepted (recon only, no production diff) |
| Date        | 2026-09-16                     |
| Milestone   | M0142 (filed under M0142-0003f's secondary finding) |
| Filed by    | M0142-0003f's secondary, untraced finding |
| Files       | M0143-0008 (fix: extend rollback-undo to non-CREATE DDL) |

## Task

M0142-0003f observed, as a side effect of its FK-add work on the shared
`:65433` bench cluster: a `psql` session ran `BEGIN;` then 11
`ALTER TABLE ... ADD CONSTRAINT ...` statements intending a later `ROLLBACK`;
the client was killed before `ROLLBACK` was reached, yet a fresh session
immediately showed all 11 constraints already committed. Filed as -0003h to
pin whether this was a per-statement auto-commit bug or an artifact of that
one interrupted run — resume point: `BEGIN; ALTER TABLE <throwaway> ADD
CONSTRAINT ...;` then probe visibility from a second session **before**
`ROLLBACK`, then again **after**.

## Method

Built HEAD (`78f67549d`, includes -0003g) to a private binary
(`/tmp/goopg-0003h-bin`, `go build ./cmd/goopg`) and ran a throwaway cluster
(`/tmp/goopg-0003h-repro`, port 5533, `GOOPG_CG_UNIT=goopg-0003h-repro`,
memory-capped per `scripts/goopg-test-run.sh`) — never touched the shared
`:65433` cluster. Two `psql` sessions: session A driven through a named pipe
so its transaction stays open across probes from session B (a fresh `psql -c`
per check, i.e. a genuinely separate backend/snapshot, not the same session
re-querying itself).

Three repros, each `BEGIN;` then one DDL statement, probed from session B
before and after `ROLLBACK;`:

1. **`INSERT INTO t VALUES (1);`** (DML control) — session B sees 0 rows both
   before and after `ROLLBACK`. Correct MVCC isolation; confirms the harness
   itself is sound and the transaction machinery (`BEGIN`/`ROLLBACK`) works
   for ordinary DML.
2. **`ALTER TABLE t ADD CONSTRAINT t_check CHECK (id > 0);`** — session B
   sees the constraint in `pg_constraint` **before** `ROLLBACK` (premature
   cross-session visibility) **and still sees it after** `ROLLBACK`
   (rollback did not undo it at all — a permanent, silent commit regardless
   of the transaction outcome).
3. **`CREATE TABLE t_new (id int);`** — session B sees the table in
   `\dt` **before** `ROLLBACK` (same premature-visibility symptom as #2) but
   the table **is gone after** `ROLLBACK` (rollback correctly undoes it,
   unlike #2).

## Root cause

Traced via the existing rollback-undo mechanism
(`internal/executor/operators_tx.go:253` `execRollback` →
`ProcessRollbackUndos`) and its landing design doc,
`docs/design/0000-0049/0030-0006-transactional-ddl.md` ("Phase 1", 2026-05-04):

- The undo list (`session.go`'s `DDLUndoEntry`/`RecordDDLCreate`/
  `TakePendingDDLCreates`) is populated at exactly **six** call sites in
  `internal/executor/operators_ddl.go` (grep `DDLUndoEntry{`: lines 4023,
  5147, 5427, 20129 for `CREATE TABLE`-shaped statements; 14228, 14439 for
  `CREATE INDEX`). **No other DDL form ever calls `RecordDDLCreate`.**
- Phase 1's own "Known Limitations" section states this in scope terms:
  *"DROP TABLE inside a ROLLBACK is not yet supported"* and *"Concurrent DDL
  visibility ... deferred"* — but does not call out that **`ALTER TABLE`
  subcommands (ADD CONSTRAINT, ADD COLUMN, DROP CONSTRAINT, ...) were never
  in Phase 1's scope at all**, so they silently behave as if every DDL
  statement auto-commits, with no undo path and no warning.
- Repro #3 (`CREATE TABLE`) is undone correctly because it **is** covered by
  the six call sites; repro #2 (`ALTER TABLE ADD CONSTRAINT`, which persists
  via `syncConstraintCatalogRow`, `operators_ddl.go:12912`) is not, so its
  mutation to the shared `catalog.InMemory` structure is never reverted.

## Two distinct findings, not one

1. **Premature cross-session visibility (repros #2 and #3, both DDL forms)**
   — this is the **already-known, already-documented** Phase 1 limitation
   ("Concurrent DDL visibility ... deferred", `0030-0006.md`). `catalog.InMemory`
   is one process-global shared structure (confirmed independently by memory
   `goopg_no_runtime_shared_catalog_inplace_update`); DDL mutations are not
   gated by any per-transaction snapshot the way ordinary heap row visibility
   is (repro #1 proves the row-visibility path itself is correct). Not new,
   not re-opened here — just re-confirmed as still live.
2. **`ALTER TABLE ADD CONSTRAINT` (and, by the same structural argument, any
   other `ALTER TABLE` subcommand) has NO rollback-undo at all** — this is
   the actually novel, more serious finding: `ROLLBACK` returns success but
   silently leaves the constraint permanently committed. Unlike finding 1
   (a documented gap with an understood, bounded blast radius — later reads
   are still correct once the transaction concludes), this one leaves
   **permanently wrong catalog state** after a statement the client
   explicitly asked to discard. This is what actually explains -0003f's "11
   statements committed despite the intended ROLLBACK never being reached"
   observation — not a per-run fluke, a structural gap that reproduces on a
   clean, isolated cluster every time.

## Verdict

-0003h's own question ("does the commit happen per-statement, or is this one
interrupted run") is answered: **per-statement, deterministically, for any
non-`CREATE TABLE`/`CREATE INDEX` DDL** — specifically because
`RecordDDLCreate` was never extended past Phase 1's original two forms. No
code changed this loop (recon-scoped, matching this milestone's established
pattern of recon → filed fix, e.g. -0003a through -0003g). Follow-up filed as
**M0143-0008** (`.ralph/fix_plan.md`) under the Engine-correctness-carryover
milestone, since this is a general transactional-DDL defect unrelated to
join-order costing, not an M0142 item.

## Verification

Manual two-session repro only (throwaway cluster, described above; server and
data dir removed after). No unit test added — this is a recon-only task; the
fix task (M0143-0008) inherits a regression-test obligation
(`TestTransactionalCreateTableRollback` et al. in
`transactional_ddl_test.go` is the existing precedent to extend).
