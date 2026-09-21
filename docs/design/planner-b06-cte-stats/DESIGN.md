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
(`internal/optimizer/joinsearch.go:520-526`, `initialRelRows`'s `rows<=1` arm) falls back to the
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


## Step 2 slice 1 — the ndistinct consumer, and what it measured (2026-09-21)

Step 1's synthesis landed inert. This slice gives it a consumer, and the
measurement it produced re-scopes the rest of step 2.

### What landed

- **Identity and lifetime, realised without a map.** The design specifies a
  registry keyed by `DeclKey()` (`declPos:name`) on the planning context. In
  the tree, `CTEScan.cte` already points at the `*plannedCTE`, and
  `synthesizeCTEStats` takes exactly that. The entry POINTER is a strictly
  stronger identity than the key, with precisely the lifetime the design asks
  for — per-`Plan()` call, dangling never, because the body is planned inside
  the same call. So the synthesis is memoized on the entry
  (`plannedCTE.outputStats()`) and no registry is needed: the thing the key
  would look up is already in hand. The design's constraints are kept —
  one synthesis per CTE, so both references of a multi-reference CTE agree by
  construction, and a miss yields today's defaults rather than a guess.
  (Recorded as a deliberate, documented divergence, not a silent one.)
- **The consumer**: `cteSynthNDistinct` walks the index-preserving wrappers
  down to a `*CTEScan` and returns that output column's synthesized ndistinct,
  clamped to the body's row estimate. It is wired as the LAST arm of
  `columnNDistinctForChild`, after `resolveBaseColumn` and
  `groupUniqueNDistinct`, so a real catalog resolution always wins. Every step
  is fail-closed: unrecognised wrapper, out-of-range index, unpopulated entry,
  computed `Project` target, or an `unknown` column all decline. The path can
  only ever REPLACE a default with a derived number; it can never invent one.

### What it measured — the slice is INERT on the corpus, and why

TPC-DS SF0.25, default arm: **99/99 plans identical**, `MISMATCH=0`. That is
not "safe and therefore fine" — it needed explaining, so the consumer was
instrumented:

| observation | count per corpus run |
|---|---|
| `outputStats()` computed | 23 |
| consumer reached a `*CTEScan` and asked for a column | **852** |
| … the column was **unknown** (body shape unclassified) | **648** |
| … the column was a **group key** (classified, not numbered) | **204** |
| … the column was an **agg output** | **0** |
| … the column was a **union literal** | **0** |

So the consumer is live and exercised 852 times, and declines every time for a
correct reason. The decisive finding is the last two rows: **the two column
kinds the landed synthesis can actually number are never requested.** The
agg-output FD bound (gap G3) — which is what step 1 delivers — has no consumer
at all in the ndistinct channel on this corpus.

### Consequence for the rest of step 2

The value is entirely in the columns consumers do ask about:

1. **Group keys — 204 asks, classified but unnumbered.** These need the
   group-combo rule: per-column ndistinct from the group combo clamped by
   output rows (gap G2, and the synthesis's own step 3). This is the next
   slice and it is where any plan movement on the `year_total` shapes will
   come from.
2. **Unknown — 648 asks.** The synthesis does not classify these body shapes
   at all. Before widening it, census WHICH shapes they are; 648 is large
   enough that guessing would be expensive.

Neither was attempted here. Wiring the group-combo rule blind — without the
consumer path proven and without knowing which kinds are actually requested —
is how a statistics change moves default-arm plans on an unmeasured mechanism.
That path is now proven, and the target is now named.


## Step 2 slice 2 — the group-combo rule (gap G2), and the census that preceded it

### The 648 `unknown` asks, censused first

Slice 1 left 648 asks per corpus run landing on body shapes the synthesis does
not classify. The task's own ordering said census before widening, and the
census changed what "widening" should mean:

| CTE body shape | unknown asks / run |
|---|---|
| `Project(WindowAgg)` | **366** |
| `Project(Filter)` | 158 |
| `SetOp` (bare) | 59 |
| `Project(SetOp)` | 41 |
| `DistinctOn` | 24 |

Two of these are worth recording as findings rather than just counts. Window
functions are 56% of the unknown population and the synthesis has no
`WindowAgg` rule at all. And `Project(Filter)` — a plain filtered select —
appears 158 times, which this document's own G1 note says should not happen
("plain bodies need no synthesis: the existing resolver already recurses into
CTE bodies for those shapes"); the consumer is reached only when
`resolveBaseColumn` FAILS, so for those 158 the resolver is not doing what G1
assumes. Both are unresolved and ledgered.

### The rule

A grouping column's distinctness in the aggregate's OUTPUT is bounded twice:
by its INPUT distinctness (grouping emits a subset of values it already had)
and by the GROUP COUNT (one output row per group). The rule is the minimum —
**and it applies only when the input ndistinct is known**.

### Why the group count is NOT a usable fallback (measured, not reasoned)

The first version of this rule used the group count when the input was
unknown. Sound as a *bound*; wrong as an *estimate*. For one key of a
multi-key grouping, a low-cardinality column gets priced at the whole group
count — TPC-DS `d_week_seq` (a few hundred weeks) grouped alongside a store
key is priced in the tens of thousands. That inflates the key's ndistinct,
which deflates the join selectivity that divides by it.

The default-arm sweep caught it immediately:

```
Q59  before:  Hash Join   (cost=4460.71..6983.92 rows=43)
     after:   Nested Loop (cost=1925.94..6878.78 rows=1)   <- estimate collapsed
```

Values stayed correct (`MISMATCH=0`), so only the PLAN channel could see it —
a concrete instance of why this task gates the default arm and reports plan
movement explicitly. Requiring a known input makes the rule strictly a
tightening of a value the estimator already had.

### Result: correct, exercised, and plan-neutral

Against the TRUE pre-change baseline (not the intermediate buggy capture — the
baseline-drift trap this document records): **99/99 plans identical**,
`PASS=96 MISMATCH=0`, TPC-H acceptance arm 24/24.

The rule is not inert in the sense slice 1 was. It **fires 18 times per corpus
run**, and where it fires the correction is large:

```
in=4      groups=655237   -> nd=4        (vs defaultNumDistinct 200)
in=6      groups=356      -> nd=6
in=39504  groups=3256     -> nd=3256     (group-count bound wins)
```

So 18 columns now carry a derived ndistinct instead of a default, several of
them 50x tighter — and no corpus plan at SF0.25 depends on those columns
today. That is a safe landing, not a valuable one: the value is banked for the
residuals this task exists to unblock, which can now be re-evaluated against
derived rather than default estimates.

### Not done here

A formal EA (estimate-audit) ratchet on the `year_total` shapes was NOT run;
the evidence above is the plan channel plus the fire census. Since plans are
byte-identical the *chosen* plans are unchanged, but estimate QUALITY on those
18 columns did change and is unmeasured by q-error. Ledgered.


## Step 2 slice 3 — the WindowAgg arm, and the finding that redirects this task

### The arm

`Project(WindowAgg)` was 366 of the 648 unclassified asks (56%), so it was the
next slice by size. The rule is stronger than the group-key one: a `WindowAgg`
is ROW-PRESERVING — one output row per input row, publishing
`child row ++ func outputs` — so a column it hands through does not merely
inherit a BOUND from its input, it has the input's distinctness exactly. No
clamp is correct. Window-function outputs themselves stay unknown.

Landed with three pins (pass-through with reordered targets, fail-closed on a
computed target and on unanalysed input, and a dispatch-order pin that the
window arm never overwrites an earlier rule's record). Gates green: TPC-DS
SF0.25 default arm `PASS=96 MISMATCH=0`, **plans 99/99 identical**, TPC-H
acceptance arm 24/24, spotcheck Q12=2 Q13=33.

### It fires ZERO times — and the reason redirects the task

Instrumenting the arm across a corpus run:

```
synthWindowOutputs calls           23
  declined at the map stage        19   <- 15 Project(non-window), 3 SetOp, 1 DistinctOn
  entered                           4   <- the real window bodies
per-column outcome inside those 4:
  decline=nd (input unresolvable)  19
  decline=pos (window func output)  4
  succeeded                         0
```

The 19 map-stage declines are correct: those bodies are not window bodies at
all. Only **four** CTE bodies in the corpus are genuinely
`Project(WindowAgg)`-shaped, and they generate all 366 asks between them.

For those four, every pass-through column declines for one reason: the input
ndistinct is unresolvable. The `WindowAgg`'s child is always a `*Sort` (R6
stacks it for presorted input), and beneath that Sort:

```
15  *optimizer.WindowAgg     <- stacked OVER clauses
 4  *optimizer.Aggregate
```

So `columnNDistinctForChild` cannot cross a `WindowAgg` or an `Aggregate` to
reach the base statistics — **which is gap G1 itself**, one level below the CTE
boundary rather than at it.

### What this means for the remaining work

This is the third consecutive slice to land correct, pinned, safe and INERT,
and the census now explains the pattern. The blocker is NOT a shortage of
synthesis rules at the CTE boundary. It is that the resolver beneath the
boundary stops at `Aggregate`/`WindowAgg`, so whatever rule is added at the
boundary asks for a number that cannot be produced.

Upstream has no equivalent problem: `examine_simple_variable`
(`postgres/src/backend/utils/adt/selfuncs.c`) resolves a subquery-output Var to
the underlying relation's `pg_statistic` row regardless of the nodes in
between, which is why PG needs no per-shape synthesis at all.

**The next slice should therefore be G1 proper** — teach `resolveBaseColumn`
(`internal/optimizer/joinkeyproof.go`) to cross an `*Aggregate` (a group key
resolves to its input column; an agg output does not resolve) and a
`*WindowAgg` (a pass-through column resolves to its input column; a func output
does not) — not another boundary rule. The boundary rules already landed are
the consumers that G1 will finally feed; adding a fourth before G1 lands would
be a fourth inert slice.
