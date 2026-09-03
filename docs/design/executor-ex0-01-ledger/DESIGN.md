# EX0-01 Design — File the executor backlog ledger

Item: `TODO_EXECUTOR.md` EX0-01 (13 §2.2; gate: ledger rows merged, no code).
Status: design for review; no behaviour change in this item by construction.

## 1. Problem

12 §0 records that the take2 P7-03 executor rows the plan bundle expected do
not exist: grep over `.ralph/deferral_ledger.md` finds exactly one matching
row, `take2-executor-residual` (Q9 equal-cardinality width residual). The
"11 rows" referenced by 07 §6 / 08 §1 P6 live in take2's TODO, not in this
repo's ledger. EX0-01 files one ledger row per open 12 gap (G-EX1…G-EX8),
each carrying its 10 § upstream citation, so every later EX item resumes
from a filed row instead of a forward reference (ground rule 7).

## 2. Row set (8 rows, keyed `take3-EX0-<gap>`)

Ledger schema: `| status | date | task-id | landed | deferred | resume point | why |`.
`landed` carries what prior takes already closed (so the row cannot be
misread as claiming untouched ground); `deferred` is the open scope;
`resume point` names the TODO_EXECUTOR item that executes it.

| task-id | landed (context) | deferred | 10 § cite | resume point |
|---|---|---|---|---|
| take3-EX0-G-EX1 | take5 6-of-16 prefilter deform (width reduction landed; 12 §3, §11 — the numeric text-decode 2.0× belongs to G-EX6, not here) | general `slot_getsomeattrs` analogue (stop at highest referenced attribute), per-attribute lazy detoast | 10 §§3, 13, 15 (homo; `dense_alloc` packing context is 10 §6 but executes as EX3-02 under G-EX3) | EX1-02 then EX1-03 (after EX1-01); planner P4-01 target lists are the general fix's input |
| take3-EX0-G-EX2 | take5 clone-after-filter; take6 one-copy `PageGetHeapTupleInto` + boxing removal (12 §4, §11) | ownership passing at join/agg/gather seams, `acquireRow` pool sizing | 10 §§3, 13, 15 | EX2-01 audit first, then EX2-02a/02b/02c, EX2-03 |
| take3-EX0-G-EX3 | — (measured only: ~18M pool round-trips, ~2×2.3 GB on Q9-class; 12 §5) | probe re-materialisation cascade + 2× build memory (dense-chunk builds) | 10 §§5, 6 | EX2-02a (cascade shrinks there) then EX3-02 |
| take3-EX0-G-EX4 | `runtime.Stack` elimination `1d6b1e396` (Stack-walk projections superseded; 12 §6, §13) | spill run/batch discipline per PG semantics + residual encode/I/O pricing with reader-path audit | 10 §§6, 9, 11, 16 | EX3-01 re-measure, then EX3-03/EX3-04; never gated on Q14/Q3/Q10 (STALE, 12 §13) |
| take3-EX0-G-EX5 | take7 compiled scan prefilter 1.20× (12 §7, §11) | `filterOp`, join residual + per-algo key evaluators, agg `transfn` compilation (serial first) | 10 §12 (expression JIT oracle; agg transfn mechanics additionally 10 §8) | EX4-01/02/03; EX4-04 waits on EX5-01 |
| take3-EX0-G-EX6 | numeric decode half 2.0× (12 §8, §11) | text/date/TOAST-pointer decode remainder past the numeric fast path | 10 §§4, 15 | EX1-01/EX1-02 decode slices; if EX1 leaves a decode residual, file a follow-up row keyed `take3-EX1-decode-residual` before EX3 geometry work starts |
| take3-EX0-G-EX7 | — (measured: q16 34% in `lessRows`; 12 §9) | bounded top-N, incremental sort, skew partitioning, abbreviated keys, logtape merge | 10 §§6, 8, 9, 16 | EX3-04/05/06; planner P4-04/P4-05 halves are the paired input |
| take3-EX0-G-EX8 | parallel shared-build mechanics (12 §10; the take6 CLOG/atomics win is a §11 closed item, not parallel scope) | `Gather` slab parity, shared-build hardening, worker stats, GatherMerge/exchange; AIO `ReadStream` as a measurement item (no 10 § oracle covers AIO — 11 §14 / 13 §7 scope, executed as EX5-04) | 10 §10 | EX5-01 (unlocks EX4-04), then EX5-02/03; EX5-04 as A/B-or-decline |

Cross-reference (not a new row): `take2-executor-residual` stays as history;
its resume ("planner P4-01") is now G-EX1's resume chain above.

## 3. Why rows, not code

EX0 moves no behaviour (TODO_EXECUTOR.md EX0 exit). The gate is "ledger rows
merged, no code" — review checks each row for: (a) a real 10 § citation
(verifiable in `10-executor-pg-design.md`), (b) a resume point naming an
existing TODO_EXECUTOR item (or an explicit follow-up promise, G-EX6), (c) a
`landed` column that prevents double-claiming take5/6/7 wins. No timing is
claimed; no plan can move (docs-only diff).

## 4. Verification

- `git diff --stat` shows only `.ralph/deferral_ledger.md` (+8 rows) for the
  implementation commit.
- Each `task-id` greps exactly once in the ledger (no duplicates).
- TODO_EXECUTOR.md EX0-01 line rewritten to the `[x]` closed form with the
  commit hash.
