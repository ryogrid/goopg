Task: M0142-0003h — pin root cause of -0003f's "ROLLBACK never reached but
DDL committed" observation. DONE this loop (recon-only, no production diff).
Follow-up (the actual fix) filed as M0143-0008.

Files: `.ralph/fix_plan.md` (M0142-0003h marked `[x]` with findings; new
`M0143-0008` filed under the engine-correctness-carryover milestone).
`.ralph/deferral_ledger.md` (new row, task-id `m0142-0003h`).
`docs/design/0100-0149/m0142-0003h-ddl-rollback-undo-scoped-to-create-only.md`
(new). `docs/design/README.md` (indexed). No Go/production code touched —
pure recon on a private throwaway cluster (built, used, fully torn down;
never touched the shared `:65433` bench cluster, which is still down per
M0142-0003k(c), still blocked on a human decision).

Key symbols/paths: `internal/executor/operators_tx.go:253` (`execRollback`),
`internal/executor/session.go` (`DDLUndoEntry`/`RecordDDLCreate`/
`TakePendingDDLCreates` — populated at exactly 6 call sites in
`operators_ddl.go`, all `CREATE TABLE`/`CREATE INDEX`, grep `DDLUndoEntry{`
to re-find them). `internal/executor/operators_ddl.go:12912`
(`syncConstraintCatalogRow` — the ADD-CONSTRAINT mutation with no undo
counterpart). `docs/design/0000-0049/0030-0006-transactional-ddl.md` (Phase-1
scope statement: only CREATE TABLE/INDEX ever got rollback-undo;
"concurrent DDL visibility... deferred" is the separate, already-known gap
— do not conflate with the new finding).

Findings this loop: two-session repro (session A held open via a named pipe,
session B a fresh probe backend) on `/tmp/goopg-0003h-repro` (:5533, HEAD
binary `78f67549d`, since fully torn down). **DML control** (`INSERT`) rolls
back correctly — ordinary MVCC row visibility is sound, this is a DDL-only
gap. **`ALTER TABLE ADD CONSTRAINT`**: visible to session B before
`ROLLBACK`, AND still present after `ROLLBACK` — never undone at all, a
silent permanent commit. **`CREATE TABLE`**: also prematurely visible before
`ROLLBACK` (same symptom, but this half is the ALREADY-DOCUMENTED Phase-1
"concurrent DDL visibility deferred" limitation, re-confirmed not new) yet
correctly gone after `ROLLBACK` (it IS covered by the 6 undo call sites).
Root cause for the novel half: `RecordDDLCreate` was simply never extended
past `CREATE TABLE`/`CREATE INDEX` in Phase 1, so every other DDL form
(confirmed for ADD CONSTRAINT; structurally implied for all `ALTER TABLE`
subcommands by the same code read) has zero rollback-undo.

Next step: **M0143-0008** (filed, not started) — extend rollback-undo to
`ALTER TABLE` subcommands, starting with ADD/DROP CONSTRAINT. Likely overlaps
`M0143-0002`'s DROP-CONSTRAINT-FK bug (`execAlterTableDropConstraint`,
`operators_ddl.go:13259`) — triage together before implementing, since both
sit in the same subsystem. Test precedent: `transactional_ddl_test.go`'s
`TestTransactionalCreateTableRollback` et al.
Separately, **M0142-0003k(c)/M0142-0003i remain BLOCKED** on a human
decision (drop the shared `:65433` cluster's 12 orphaned scratch tables +
HammerDB SF=1 reload + re-land 11 FK/PK constraints) — nothing changed on
that front this loop; do not re-attempt without explicit authorization, per
last loop's finding that the auto-mode classifier declines it.

Gates run: `make ralph-state-guard` — same pre-existing stale
status/progress marker as recent loops (loop_count field lag), self-repaired,
passed clean. No `go build`/`go test` run (no Go/production code touched this
loop — pure recon + docs/fix_plan/ledger updates).

In-flight: none. The throwaway repro cluster (binary `/tmp/goopg-0003h-bin`,
data dir `/tmp/goopg-0003h-repro`, cgroup unit `goopg-0003h-repro`, port
5533) was stopped cleanly (`goopg stop -mode fast`, exit "server stopped")
and all its files removed before this loop ended. Nothing left running.
