# B-06 (P1-27) — CTE-output statistics synthesis

Status: accepted (design only — step 1 of the ledger's 4-step resume).
Implements TODO_ALL.md B-06 (DEFERRED-OPEN → design stage). Ledger
`take3-B-06-deferred` (guard load-bearing: removal reverts Q74 to 99s;
PG has no answer either — single-key uniqueness only, Q74 groups by 4).

## 1. Problem (surveyed 2026-09-06)

Q74 (`bench/tpcds/runtime_goopg/tpcds-data/queries/query74.sql`):
`year_total` = UNION ALL of two 4-key GROUP BYs; outer 4-way self-join
on `customer_id` + restrictions. CTE scans carry no per-column
statistics, so `filterSelectivity` charges `defaultEqSelectivity`
(0.005) per conjunct: 4 conjuncts over 2-valued columns →
`0.005⁴×17977 ≈ 0.000011`, collapsing to 1 row and making nested
loops look free. The `rows<=1` guard
(`internal/optimizer/joinsearch.go:470-476`) falls back to the
unfiltered `EstimateRows(cte.Child)` — load-bearing, removal reverts
the win.

Three precise gaps (all verified in-tree):
- **G1 — no CTE-output stats.** `EstimateRows(CTEScan)` passes through
  to the body (`internal/optimizer/cardinality.go:123-130`);
  `resolveBaseColumn` recurses into CTE bodies
  (`internal/optimizer/joinkeyproof.go:165-168`) but has no
  `*Aggregate`/`*SetOp` arm, so aggregate/set-op outputs resolve to
  nothing → nil stats → defaults
  (`internal/optimizer/selectivity.go:264-271`,
  `internal/optimizer/cardinality.go:886-916,1510-1518`).
- **G2 — OID-keyed registries exclude CTE outputs.**
  `groupVarInfo.tableOID` stamps only on resolver hit (0 = unknown,
  fail-closed); CTE-output grouping keys fall to coordinate-only
  `groupVarKey` with `defaultNumDistinct`
  (`internal/optimizer/cardinality.go:1328-1354`);
  `groupComboNDistinct` declines `oid==0`
  (`internal/optimizer/cardinality.go:1390-1404`) — Q74's 4-key combo
  can never match.
- **G3 — no FD bound for agg outputs.** Only the exact-set
  multivariate hit and single-key `groupUniqueNDistinct` bound agg
  outputs (`internal/optimizer/extstats.go:157-193`,
  `internal/optimizer/joinkeyproof.go:196-261`); nothing bounds an
  agg-output ndistinct by its group count.

## 2. Synthesis design (4 steps — this doc is step 1's spec)

**Identity (no OID): `DeclKey()` and nothing else.** Key synthesized
stats by the executor's `DeclKey()` (`declPos:name`,
`internal/optimizer/plan.go:1674-1680`, PG `ctePlanId` analogue) —
`declPos` is the parse source offset (stable within one statement),
`name` the CTE name. NOT `(declSeq, lower(name))`: `planCTEs` is keyed
by lower name only (`internal/optimizer/with.go:144,194`), `declSeq` is a process-global
monotonic never reset (`internal/optimizer/with.go:60-69`), and `DeclKey` is
`declPos:name` — three different namings, and only `DeclKey()` is
also the executor `CTERowCache` key (`internal/executor/operators_cte_dml.go:380-382`,
`internal/executor/context.go:657`), which the synthesis registry must parallel
(same CTE, same key, both sides). A consumer always names the
*referenced* decl (mutual sibling reference is impossible —
left-to-right visibility, `internal/optimizer/with.go:199-209`).

**Lifetime: per-`Plan()` call, on the planning context** (beside
`planCTEs`, not beside the global `plannerExtStatsByTable`): global
scope would grow unbounded on the monotonic counter AND collide
`(declPos,name)` across statements; per-plan scope dangles never
(the body is planned inside the same call). Recursive self-reference
(stamped zero/zero, `internal/optimizer/with.go:302-307`) and costing inside a body
before its own post-plan synthesis are EXCLUDED (miss → today's nil).

**What is synthesized, per CTE, at plan time** (when the body is
planned — bodies preplan left-to-right before outer costing
(`internal/optimizer/with.go:211`), and each consumer holds its own `Child` clone so
`EstimateRows(Child)` is synchronous; recursive/self and
in-body-costing positions excluded above):
- `rows`: body estimate (`EstimateRows(body)` — same number the
  guard reads today, promoted from fallback to first-class).
- Per output column: `ndistinct` by rule —
  (a) passthrough column (body output, unmodified): the body's own
  per-column ndistinct when resolvable, else `defaultNumDistinct`.
  Through Agg/SetOp bodies this needs the G1 arms plus union
  semantics AND, for sibling-referencing bodies, the sibling's combo
  stats — i.e. rule (a) depends on the B-05c subset/superset
  remainder *through CTE bodies*, ordered before sibling synthesis;
- (b) UNION-branch literal column (Q74's `sale_type` — the actual
  nd=2 producer): each branch contributes its literal set (usually
  one value); the union's ndistinct is the distinct-literal count
  across branches (2 for `{'s','w'}`). First version: literals only,
  no expression folding;
- (c) grouping key with restriction-derived bound (Q74's `year`):
  restriction literals on the key (`IN`/equality lists visible at
  the consumer or pushed into the body) bound ndistinct to the
  literal count; else rule (a) through the group-by input where
  resolvable, else `defaultNumDistinct`. Clamp-by-output-rows alone
  is NOT a rule (vacuous: `min(200,17977)=200` reproduces today's
  default bit-for-bit, and `min(rows,…)` from a rows base is worse)
  — stated so no implementation ships it as progress;
- (d) aggregate output: FD-bound by the group count (step 3 details),
  bare agg-output columns only (not expressions over them; grouping
  sets included); never distributed (matching PG). Sound for every
  per-group scalar agg (`count(distinct x)`, `max`, `array_agg`,
  FILTER/DISTINCT variants): one output row per group ⇒ ndistinct ≤
  group count; grand-total single group ⇒ 1, correct.
- Explicit NON-synthesis: MCV lists, histograms, nullfrac beyond
  0/1-presence (declined — unbounded memory for unprincipled gain;
  revisit only with a measured restriction-selectivity win).

**Consumers (wiring points, behavior-neutral until step 4 arms them):**
`resolveBaseColumn` returns OID-anchored refs, so OID-less stats need
a PARALLEL synthetic path, not an extension: a `*catalog.ColumnStats`
synthesized from the registry entry, consumed by
`eqSelectivityForColumn` (replaces 0.005 with `1/nd` where known —
the Q74 collapse fix: `sale_type` nd=2 and `year` nd=2 turn
`0.005⁴×17977` into `0.5²×…`-scale estimates that no longer collapse
to 1). Miss → today's nil — no behavior change on miss.
`groupComboNDistinct` needs a SECOND registry beside `map[uint32]`
(`internal/optimizer/extstats.go:109`) for OID-less exact-set match on
`(DeclKey,attnos)`; all other declines stand.

**Step 4 (close condition, binding — falsifiable):** the guard is
removed iff ALL hold (else it stays, no partial credit):
(a) the Q74 filtered CTE scans estimate within a stated factor
(≤10×) of actual rows with NO fallback firing (assert the fallback
counter stays zero on Q74 — plan-shape pin, not wall-clock);
(b) the all-CTE-query sweep passes `CKMISMATCH=0` (every query with a
CTE scan — the guard fires statement-wide on ANY CTE scan, so a
single-query gate is insufficient); (c) TPC-DS sweep green.
Wall-clock ("same 14s regime") is evidence, never the criterion.
Guard removal is the B-06 close line, not an optional cleanup.

## 3. Non-goals and dependency order

- Rule (a) through Agg/SetOp/sibling-referencing bodies needs the
  G1 arms + union semantics + the B-05c subset/superset remainder
  through CTE bodies, ordered BEFORE sibling synthesis (dependency
  chain stated, not solved here).
- Modifying `groupComboNDistinct`'s OID factor for base tables.
- ANALYZE-time CTE stats (planning is per-query; no catalog writes).
- Touching the guard in this design (steps 2–4 implement; the guard
  stays until step 4's criterion holds).

## 4. Gate (this design)

Review only (this file). Implementation gates (steps 2–4, for the
record): unit (synthesis rules incl. miss→nil; identity collisions);
EA ratchet on year_total shapes; TPC-DS sweep `CKMISMATCH=0`; guard
removal with before/after on Q74.
