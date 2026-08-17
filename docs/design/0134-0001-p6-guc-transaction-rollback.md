# M0134-0001 P6 — plain `SET` must be undone by transaction ABORT

status: accepted
milestone: M0134-0001 (`aggregates.sql` regress digestion), slice **S15**
date: 2026-08-17

## Summary

`BEGIN; SET x = v; … ROLLBACK;` leaves `x = v` in effect in goopg. In
PostgreSQL the abort reverts it. This is a **general GUC/transaction-semantics
defect**, not a parallel-query defect — it was found while bucketing the
`aggregates.sql` diff, where it masquerades as two "goopg over-parallelises"
plan-shape hunks.

## How it was found (and the misdiagnosis it corrects)

`.ralph/working_set.md` after S14 bucketed the remaining 30 `aggregates` hunks
and named "parallel 5" as the next slice, on the theory that
`computeParallelWorkers` / the Gather-insertion site disagree with PG's
`min_parallel_table_scan_size` / `parallel_setup_cost` rules. Research
(`tmp/ralph-handoffs/m0134-0001-s15-parallel/report.md`) refuted that for the
two largest hunks:

`aggregates.sql:1448-1488` is a `BEGIN; … ROLLBACK;` block whose first five
statements are **plain `SET`** (not `SET LOCAL`):

```sql
BEGIN;
SET parallel_setup_cost = 0;
SET parallel_tuple_cost = 0;
SET min_parallel_table_scan_size = 0;
SET max_parallel_workers_per_gather = 4;
SET parallel_leader_participation = off;
…
ROLLBACK;
```

By `aggregates.sql:1544`/`:1580` (the `agg_data_20k` `GROUP BY` EXPLAINs, a
later section that sets **no** parallel GUCs of its own) PG is back on defaults
— `parallel_setup_cost=1000`, `min_parallel_table_scan_size=8MB` — and declines
to parallelise a tiny single-column 20k-row table. goopg still carries the
leaked `min_parallel_table_scan_size=0` / `max_parallel_workers_per_gather=4`
and emits `Gather Merge` / `Gather` + `Workers Planned: 4`.

So goopg's parallel planner is **doing the right thing for the session state it
believes it is in**. `computeParallelWorkers`
(`internal/optimizer/parallel.go:481-529`) is a faithful port of PG's log₃
worker scaling and needs no change for these hunks. The bug is one subsystem
away, in the GUC session registry.

This is the second time in this milestone that a plan-shape hunk turned out not
to be a planner bug (cf. P2/S11, where an "underline-width cosmetic gap" was
really cumulative EXPLAIN indentation). Bucketing a regress diff by the *shape
of the divergent output* systematically over-attributes to the subsystem that
prints the output.

## PostgreSQL's rule

PG keeps a per-variable stack of prior values (`GucStack`, `guc.c`), pushed by
`set_config_option()` when the change happens at a transaction nest level above
the value's current level, and unwound at end of transaction by

```
AtEOXact_GUC(bool isCommit, int nestLevel)
```

called from `xact.c` — with `isCommit = false` on abort. On abort every entry
recorded at the aborting level is **restored to its prior value**; on commit
the new value is kept (for `GUC_ACTION_SET`) or discarded (`GUC_ACTION_LOCAL`).
The crucial point for this slice: **plain `SET` is stacked too**, not just
`SET LOCAL` — the difference between them is what happens at *commit*, not at
*abort*.

Docs: `postgres/official_docs_in_md/` — SET(7), "the effects of SET … are
rolled back if the transaction is later aborted".

## goopg before this slice

`internal/utils/misc/SessionRegistry` has two value layers, `s.session` (plain
`SET`) and `s.local` (`SET LOCAL`), and a flat `s.inTx` flag:

- `Set(name, value, isLocal=false)` (`session.go:114-171`) writes
  `s.session[key]` **directly and permanently**, with no undo record.
- `EndTransaction()` (`session.go:286-296`) drops the whole `s.local` map and
  clears `inTx`. It is called identically from the COMMIT and the ROLLBACK
  path — it has no way to tell them apart, because the executor hook it is
  wired to, `Context.EndLocalTransaction` (`internal/executor/context.go:398`),
  is a bare `func()`.

So `SET LOCAL` is correct by construction (dropping it at either outcome is
right) and plain `SET` is unconditionally permanent — correct at COMMIT, wrong
at ROLLBACK.

## Design

Two changes, matching PG's shape at goopg's existing flat (non-nested)
fidelity:

### 1. `EndLocalTransaction` learns the outcome

`Context.EndLocalTransaction` becomes `func(committed bool)`, mirroring
`AtEOXact_GUC(bool isCommit, …)`. Every call site passes its verb explicitly.
The sites are twins and must change together
(Hard-won Rule #2):

| site | verb |
|---|---|
| `internal/executor/operators_tx.go:215` (COMMIT) | `true` |
| `internal/executor/operators_tx.go:267` (ROLLBACK) | `false` |
| `internal/postmaster/txn_verb.go:104` | per the verb it dispatches |
| `internal/postmaster/twophase.go:228` | per the verb it dispatches |
| wiring: `internal/postmaster/dispatch.go:432`, `dispatch_extended.go:279` | forwards to the registry |

Passing the flag (rather than adding a second `AbortLocalTransaction` hook)
keeps one hook with one meaning and makes a missed site a *compile* error
instead of a silent commit-semantics fallback.

### 2. `SessionRegistry` journals plain `SET` while in a transaction

- New field `txPrior map[string]*string` — for each session-layer key mutated
  since `BeginTransaction`, the value that key held **before the first mutation
  in this transaction**. `nil` pointer = the key had no session-layer entry at
  all (so the restore must `delete`, not write an empty string; writing `""`
  would shadow the boot value).
- `BeginTransaction()` resets the journal.
- Every writer of `s.session` snapshots-once-if-absent while `s.inTx`:
  `Set` (the non-local branch), `Reset`, `ResetAll`. `SetInternal` is out of
  scope — `is_superuser` / role tracking already has its own undo
  (`connTx.LocalRolePriorValue`, M0119-0004).
- `EndTransaction(committed bool)`: drop `s.local` as today; **additionally**,
  when `!committed`, restore every journalled key (write prior, or delete when
  the prior is `nil`). Fire `onReportableChange` for `FlagReport` variables
  whose effective value actually moved, exactly as the existing local-layer
  drop does — otherwise the client's `ParameterStatus` view desynchronises from
  the server after a rolled-back `SET DateStyle`.
- Clear the journal on both outcomes.

`Set` outside a transaction is untouched (permanent, correct). A `SET` inside a
committed transaction is untouched (permanent, correct).

## Out of scope (deferred, ledgered)

**Savepoint / subtransaction granularity.** PG's `GucStack` is per nest level:
`SAVEPOINT s; SET x = 1; ROLLBACK TO s;` reverts `x`, and a `SET` made *before*
the savepoint survives that rollback. This slice's journal is flat — one level
for the whole explicit transaction — so `ROLLBACK TO SAVEPOINT` does not revert
GUCs, and the outer-transaction abort still reverts everything to the
pre-`BEGIN` state (which is right). This matches the fidelity goopg already
ships for the local layer and for `connTx.LocalRolePriorValue`. Resume point:
give `txPrior` a nest-level stack keyed off the existing savepoint bookkeeping
in `internal/postmaster/conn_tx.go`, and unwind it from the `ROLLBACK TO`
handler; PG oracle `guc.c: AtEOSubXact_GUC`.

## Measurement

`aggregates` diff at S14 = 1029 lines / 30 hunks. Expected closure: the two
`agg_data_20k` hunks (diff `:986`, `:1010`; sql 1544/1580). Sentinels that must
stay byte-identical: `functional_deps` 56, `groupingsets` 2373.

## Remaining parallel bucket after this slice (both deferred)

- **Over-broad `subtreeHasUnsafeNode` gate** — `AggregateIsOrderSensitive`
  (`internal/optimizer/parallel_agg.go:86-96`) vetoes parallelism for the
  *whole statement* when an undecorated `array_agg`/`string_agg`/`json*_agg`
  appears anywhere, where PG only refuses to **split** it (the split is already
  independently and correctly refused by `AggregateIsDecomposable`). Narrowing
  the gate closes diff `:552` (`array_dims(array_agg(s))`) fully; diff `:516`
  (`v_pagg_test`) needs array_agg/string_agg combine support in
  `internal/executor/parallel_agg_combine.go` as well — the planner whitelist
  and the executor combine dispatch are a matched pair (S10a's `balk` bug is
  what happens when only one moves).
- **No Parallel Append** — `drivingScan` (`parallel.go:344-369`) has no
  `*Append` case, so `UNION ALL`-backed aggregates never become partial-capable
  (diff `:900`, `:943`). Larger planner+executor work; tracked as S10b.

## References

- PG oracle: `postgres/src/backend/utils/misc/guc.c`
  (`GucStack`, `set_config_option`, `AtEOXact_GUC`, `AtEOSubXact_GUC`);
  `postgres/src/backend/access/transam/xact.c` (abort path).
- goopg: `internal/utils/misc/session.go`, `internal/executor/context.go`,
  `internal/executor/operators_tx.go`, `internal/postmaster/dispatch.go`,
  `internal/postmaster/dispatch_extended.go`, `internal/postmaster/txn_verb.go`,
  `internal/postmaster/twophase.go`.
- Sibling design docs: `0134-0001-p2-explain-format.md` (S10/S11),
  `0134-0001-p4-bytea-output-escape.md` (S12/S14),
  `0134-0001-p5-groupby-name-resolution.md` (S13).
- Handoff: `tmp/ralph-handoffs/m0134-0001-s15-parallel/`.
