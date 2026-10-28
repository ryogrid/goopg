# M0148 — pg_regress coverage closure with prerequisite implementation

**Filed:** 2026-10-29 (owner decision)
**Status:** planned — banner item 10 after the already-filed M0122 queue
**Coverage authority:** `docs/test-port/postgres-oracle-target-inventory.csv`
**Readable roll-up:** `docs/test-port/upstream-regress-coverage.md`
**Runner:** `scripts/pg-regress-runner.sh`

## Goal

Raise PostgreSQL 18.3 regress-suite coverage by turning every in-scope
`regress-sql` case into `status=pass` and `pass_required=yes`. M0134 previously
sized every failing case but stopped many cases after recording missing
features in `.ralph/deferral_ledger.md`. M0148 resumes from those findings and
implements the prerequisites instead of treating discovery as completion.

The two inventory rows whose policy status is `excluded` remain outside this
milestone. At filing, the inventory contains 168 `failed`, 62 `pass`, and two
`excluded` regress cases. `copyselect` is already `pass`, but remains an early
revalidation task because the owner explicitly placed it in the opening queue.

## Binding completion discipline

1. One parent task owns one regress case. Run
   `scripts/pg-regress-runner.sh -v <case>` at HEAD before changing code and
   preserve its diff as the baseline.
2. When the diff exposes a missing or incomplete goopg feature, immediately
   add an implementation child under that case task in `.ralph/fix_plan.md`.
   Every child starts with `Kind: impl` and `Parent: <suite-task-id>`, names the
   PostgreSQL 18.3 oracle function/file, and states the focused and regress
   verification that will prove it complete.
3. Implement those children. A deferral-ledger row, sizing result, `PARKED`
   label, or statement that a subsystem is missing does not complete the child
   or its parent. The parent stays open while any output difference remains.
   A true owner/external blocker may be recorded in the ledger, but still needs
   an open child task and an explicit blocker; selection resumes it when the
   blocker clears.
4. Close a suite parent only after the runner matches the PG 18.3 expected
   output. In the same commit, set its inventory row to `status=pass` and
   `pass_required=yes`, clear `deferred_to`, record the passing verification in
   `rationale`, run `make regen-testport`, and run
   `make check-testport-inventory`.
5. Non-trivial subsystem work follows `AGENT.md`: create or update a design
   document with PG source citations and index it in `docs/design/README.md`.
   Parser changes first read the parser playbook and use `make gen-parser`.
6. After closing one suite task, materialize the next queue entry in
   `.ralph/fix_plan.md` before ending the loop. Only one suite parent needs to
   be materialized at a time; implementation children stay immediately below
   their parent and are selected before the next suite.

## Canonical queue

Queue order is deterministic and is not replaced by M0134's historical task
numbers or `PARKED` labels.

The opening entries are fixed:

1. `M0148-0001` — `aggregates`
2. `M0148-0002` — `copy`
3. `M0148-0003` — `copy2`
4. `M0148-0004` — `copyselect` (revalidate the existing pass)
5. `M0148-0005` — `explain`
6. `M0148-0006` — `float4`
7. `M0148-0007` — `float8`

After those seven, take the remaining non-pass, non-excluded inventory rows in
the order shown by `docs/test-port/upstream-regress-coverage.md`, with these two
families stably moved to the end:

- penultimate family: every case whose name starts with `create_`;
- final family: character-encoding and collation cases — names starting with
  `collate`, the `encoding` case, names ending in `_encoding`, and `conversion`.

Within each moved family, preserve coverage-document order. Skip a row that is
already `pass` when reached, except for the explicitly queued `copyselect`
revalidation. Assign consecutive `M0148-NNNN` IDs when materializing entries;
do not renumber an ID already written to `.ralph/fix_plan.md`.

## Acceptance

M0148 is complete only when every in-scope `regress-sql` inventory row is
`pass` with `pass_required=yes`, `make check-testport-inventory` succeeds, and
the regenerated coverage document reports no `failed` or `not-tried` cases.
Excluded rows remain excluded by policy and do not count against completion.
