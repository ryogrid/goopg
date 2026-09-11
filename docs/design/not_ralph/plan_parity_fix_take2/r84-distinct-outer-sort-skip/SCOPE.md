# R84 SCOPE: skip redundant outer Sort over Distinct (Q41)

Lineage: R83 landed Limit-above-Distinct; Q41 keeps
`[join-order,parameterisation,sort-strategy]` from the extra
top Sort (M0097-0046) + `$0` display. This round removes the
top Sort where the Distinct output already satisfies ORDER BY.

## Step-0 findings (measured, no code changed)

- Q41's `Unique` node IS `*optimizer.Distinct` (rendered as
  "Unique", `operators_explain.go:3028`). No order-preserving
  Unique executor exists.
- Executor truth: `distinctOp` hash-dedups then ALWAYS
  re-sorts ascending, NULLs last, over ALL columns
  (`operators_distinct.go:79-97`, `compareDatum` order).
  Input order is destroyed — the inner Sort is irrelevant
  to output order. (Narrowed: `distinctOnOp` streams input
  order, but the skip site only ever sees `*Distinct` —
  enforced by a type-gate, F1.)
- F1 probe (unit, Q41 shape): node IS `*optimizer.Distinct`
  (not DistinctOn); with R83, root is already `Limit`.
  Implementation MUST `if d, ok := out.(*optimizer.Distinct)`
  else keep Sort — fail-closed.
- Redundancy rule (sound iff): every outer key is ASC with
  nulls-last AND refers to distinct-output columns as a
  positional prefix (required keys = output cols 0..n-1 in
  order). Otherwise (any DESC, NULLS FIRST, non-prefix) the
  outer Sort stays — root-0036/DESC behavior preserved by
  construction.
- Why prefix, not just direction: distinctOp orders by
  (c0,c1,…) lexicographically; ORDER BY (c1) alone is NOT
  satisfied by a (c0,c1) ordering. Prefix required.
- Blast radius census (both corpora, HEAD captures):
  exactly ONE query has Sort directly over
  Unique/Distinct — DS Q41. All other DISTINCT queries
  either lack ORDER BY or keep the Sort for direction
  reasons (verify via A/B).
- PG corroboration: PG emits no top sort over Unique
  (Unique preserves its Sort input). Within goopg (byte-wise
  comparison, no collation in either operator) the skip is
  observationally exact when the rule fires; no claim is made
  about PG order-equality beyond C collation (pre-existing
  goopg limitation, identical with/without the skip).

## Change

In the M0097-0046 block (`planner.go`, after `outerKeys`
built, before wrapping): if `out` is `*optimizer.Distinct`
(type-gate — user DISTINCT ON never reaches here; note a
plain-DISTINCT unique-candidate win builds `*DistinctOn`
(`createplansimple.go:258-259`), NOT `*Distinct`, so the
type-gate is load-bearing exactly there — declining keeps
the Sort, safe) AND every key in `outerKeys`
satisfies ASC + nulls-last (read from the effective
`outerKeys` entries — `sortByNullsFirst` already applied —
never the raw `sb`) + positional-prefix on the Distinct
output (key `i` is `ColumnRef` with `Index == i`, positions
`0..n-1` in order, `n <= len(distinctOut)`), skip the
`&Sort{}` wrap. The re-stamp runs unchanged on the new
tree; drift trips STOP.

Decline (keep Sort) on: non-`*Distinct` node; any DESC /
explicit `NULLS FIRST` (pinned) / non-ColumnRef key /
non-prefix. The `GroupingSets-adjacent` decline is dropped
(soundness never depends on the child shape — output order
erases it). All fail-closed toward today's plan.

Out: `$0` correlated display (named follow-up — PARAM_EXEC
vs outer-var rendering, misrender risk); M0097-0046
restructuring beyond the skip; join-method costing; K100.

## Predictions (recorded before implementing)

- P1: Q41 becomes
  `Limit → Unique → Sort → SeqScan` — PG's shape modulo
  `$0`. Remaining gap: parameterisation only (3 → 1).
- P2: values identical (order-preserving change on already-
  ordered output; sweep oracle adjudicates).
- P3: corpus A/B — ONLY Q41 may move; ZERO EXTRA
  (Q41-adjacent DISTINCT queries keep their Sort).

## STOP rules

- B-01c re-stamp assertion drift beyond stamp-only noise:
  stop, record (stamp is assert-only; production safe).
- Any non-Q41 shape move: stop (over-broad rule).
- Values MISMATCH: stop, scope repair (order claim wrong).
- Q41 doesn't move as P1: re-probe, don't force.

## Gates

Units, suites, synthetic order test (ASC-prefix skips,
DESC/MIXED/non-prefix keep — new pins), TPC-H spotcheck,
SF0.25 sweep (`MISMATCH=0`), byte-guard A/B both corpora +
per-query census with ZERO EXTRA, shape-delta alongside.
`match` count is NOT a criterion.
