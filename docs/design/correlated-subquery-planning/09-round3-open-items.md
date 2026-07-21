# 09 — Round 3: Closing the Earlier Rounds' Open Ledger Rows

| field | value |
| --- | --- |
| status | draft (pre-implementation design, per user direction: design before modification) |
| date | 2026-07-21 |
| branch | `planner-kaizen3` (planned; base = `planner-kaizen2` HEAD `872b424d`) |
| scope | the five still-open rows listed in round 2's report §6 "Earlier rounds' still-open rows" |
| inputs | `.ralph/deferral_ledger.md` rows (csq-S6 ×2, csq-S2/S3 ×3); fresh code investigation at HEAD `872b424d` (four parallel read-only audits + live SF1 probes, 2026-07-21) |

Round 3 closes the five rows deferred from rounds 1–2:

1. derived-table-under-cross-NL zero-rows bug (ledger csq-S6; **wrong-results class**)
2. hashed-probe family limits (ledger csq-S2/S3)
3. DML-sublink lowering (ledger csq-S2/S3)
4. LEFT+residual NLI hazard audit (ledger csq-S6)
5. composite-equijoin EXISTS decorrelation (ledger csq-S2/S3)

The pre-implementation investigation **changed the priority order**: item 4's
"latent hazard, audit only" framing is wrong — the planner emits the hazardous
shape today (§4), so it is a live wrong-results bug and goes first. Item 2's
investigation surfaced a **previously unrecorded correctness hazard** in the
existing hash probe (§2.3) that outranks the widening it was filed for.

---

## 1. Derived-table-under-cross-NL zero-rows (investigate → pin or fix)

### 1.1 Ledger claim

`FROM orders, (SELECT DISTINCT l_orderkey FROM lineitem WHERE l_commitdate <
l_receiptdate) lk WHERE o_orderkey = lk.l_orderkey` returned **0 rows fast**
on the SF1 bench server during round-1 stage 6b, while the derived subquery
alone counted 1 375 096. Observed plan: `NL (CROSS) + Filter` over `Unique`.
Ledger hypothesis: nlJoinOp inner re-Open of a Unique-rooted derived input
leaves EOF state.

### 1.2 Investigation results (HEAD `872b424d`, 2026-07-21)

- **The ledger hypothesis is structurally impossible at HEAD.** The
  non-lateral NL path (`operators_join_agg.go` `joinOp.Open`,
  :181-:212) drains both children **exactly once** into materialized row
  slices; there is no per-outer-row re-Open of the inner. `distinctOp.Open`
  (operators_distinct.go:30) fully resets accumulation state on re-Open
  anyway (the Stage-9/M12 fix).
- **In-process reproduction of the exact ledger plan shape** (NL CROSS +
  join-attached equality Filter over Unique over filtered SeqScan, forced by
  an index-free fixture with the ledger's qualification pattern —
  unqualified outer column, qualified derived column) returns **correct
  rows**.
- **On the SF1 bench server at HEAD** the same query no longer produces the
  ledger's plan: the planner now builds `Unique` as NLI **outer** with an
  `orders_pk` index probe (`Index Cond: (o_orderkey = l_orderkey)`) — a
  round-1/2-era plan improvement. A bounded variant
  (`AND l_orderkey < 1000`) returns 238 = 238 (join count equals derived
  count, each distinct key matches exactly one order): **correct**.
  Unbounded-run cross-check recorded in §1.4.

### 1.3 Design decision

Two possible worlds, both handled:

- **World A (expected): not reproducible at HEAD.** The wrong result was
  produced by a round-1-stage-6b-era plan/executor state that no longer
  exists. Action: close the ledger row as *falsified at HEAD* with the
  evidence trail (this chapter + probe outputs), and land **permanent
  regression pins** so the shape cannot silently regress:
  - unit test pinning the exact ledger plan shape (index-free: NL CROSS +
    Filter over Unique) with correct results — the shape still exists for
    index-free tables even though TPC-H no longer takes it;
  - unit test pinning the NLI-over-Unique-outer shape (indexed fixture)
    with correct results.
- **World B: reproduces at scale.** Root-cause under the executor focus the
  ledger prescribed; fix before anything else in the round (wrong results
  outrank all other items). The bounded/unbounded SF1 probes decide which
  world we are in before any code is written.

### 1.4 SF1 evidence — World A confirmed

- bounded (`l_orderkey < 1000`): derived-alone 238, join 238 — correct.
- unbounded (the ledger's exact query): derived-alone **1 375 096**, join
  **1 375 096** — equal to the ledger's recorded standalone count and to
  each other (every distinct `l_orderkey` matches exactly one order).
  The wrong result does **not** reproduce at HEAD; R3-2 executes the
  World-A action (falsification evidence + the two regression pins).

---

## 2. Hashed-probe family limits (widen safely + fix a latent hazard)

### 2.1 Current state (corrected)

Decision function `subPlanHashFamilyOf` (subplan_hash.go:91-110) with
homogeneity gate (:140-143) and operand/set family-equality gate (:216).
Investigation corrected the ledger's framing on one point: **arena-backed
strings are already hash-safe** — after the M0107-0002 layout merge there is
no `KindStringArena`; arena strings are `KindString` with `ArenaID != 0`, and
`datumKey`'s `"s:" + d.StringValue()` copies the payload to a fresh heap
string (operators_join_agg.go:3079-3080). The block comments at
subplan_hash.go:66-72/:78 claiming otherwise are stale. What actually
declines today: big-mantissa numerics, cross-family mixes, and
out-of-allowlist kinds (enum, interval, toast pointer).

### 2.2 Big-mantissa numeric → hashable (and a live wrong-results bug)

**Escalation found while implementing:** the lossy accessor is not confined
to the IN probe. `datumKey` is the canonical key for **equi-join build and
probe sides** (`evalHashKey`, operators_join_agg.go:~818) and for grouping,
so two equal big numerics stored at different mctx offsets produced
different keys and their pair was silently dropped. Measured on a 3-row
equi-join over numerics past int64: **1 pair returned instead of 3**, with
`k = <big literal>` equality correct on the same data (proving the value
and `compareEq` were fine and localising the fault to the key). The
correlated-IN form failed the same way on both the hashed and the linear
setting, because the planner decorrelates it into a semi JOIN — which uses
the same key.

This is why the fix lands in `datumKey` rather than as a probe-local
workaround: the probe widening is a by-product of repairing a join-level
wrong-results bug.


Root cause of the decline: for a `flagBigNumeric` datum,
`NumericMantissaValue()` returns `d.Int`, which in the big lane holds the
mctx offset/length encoding, **not** the mantissa — `datumKey` would hash
garbage. The linear oracle (`compareEq` → `numericCmp`) reads the true
mantissa via `numericMant`.

Fix: a big-aware sibling of `canonicalNumericKey`
(operators_join_agg.go:3110-3127): obtain the true mantissa via
`numericMant` (works for both lanes), strip trailing zero-digit/scale pairs
on the big.Int (divmod 10 while `scale>0` and low digit == 0), emit
`"m:<mantissa.String()>:<scale>"`. The int64 lane already canonicalises
`1`/`1.0`/`1.00` to one key, so big and small lanes converge on the same
canonical form — `hashFamNumeric` absorbs big numerics **without a family
split**, exactly matching `numericCmp`'s aligned-mantissa equality. Then
delete the `flagBigNumeric` decline (subplan_hash.go:96-98). Cost: one
big.Int allocation per key on the cold big-numeric path only.

### 2.3 The suspected hashFamString hazard — REFUTED by measurement

The review raised a plausible hazard: `compareDatum` applies special
normalisation for UUID / pg_lsn / row-literal / array-literal shaped
strings (expr.go:2464-2551) that `datumKey`'s plain `"s:" + value` does not
replicate, so the hashed probe could miss where the linear loop matches.

**It does not apply to this path.** The hashed probe's oracle is the linear
loop, which compares with `compareEq`, and `compareEq`'s string arm is a
plain `a.StringValue() == b.StringValue()` (expr.go:~7573) — it never
reaches `compareDatum`'s normalisations, which are ORDERING helpers
(`compareDatum` is the comparison used for sorts, min/max, and the numeric
arm). `datumKey` expresses exactly the same equality, so the two agree by
construction. Probed live on both paths with hyphen/case-varied UUIDs and
`0/10` vs `0/010` LSN strings: hashed ≡ linear.

Separately checked: uuid-TYPED columns agree with PG too, because goopg
normalises at coercion time (the same discipline as bpchar trimming), so
both paths return the match. Any residual `compareEq`-vs-PG deviation for
uuid/pg_lsn equality would affect the linear loop and the hash probe
**identically** and is therefore not a hash-probe hazard; it belongs to
`compareEq` and gets a ledger row, not a stage.

The permanent value here is a regression pin asserting hashed ≡ linear for
these string shapes, not a fix.

### 2.4 Deliberately still declined (each with reason, ledger-recorded)

- cross-family numeric-vs-text (`10 IN ('10')`): `compareEq` coerces at
  runtime, but canonical numeric and text keys can never meet without
  storing both forms per value — defeats the O(1) probe. Family gate stays.
- enum-vs-string label coercion: same argument.
- `KindInterval`: datumKey is injective, but PG interval equality has
  justify/mixed-unit subtleties not modelled; do not widen without a
  dedicated verification pass.
- `KindToastPointer`: must be detoasted first; upstream concern.

### 2.5 Tests

Extend `hashFixture` + `runBothHashPaths` (linear loop stays the oracle):
big-numeric round-trip incl. cross-lane equality in one set; UUID/LSN-shaped
text sets (must now decline; hashed ≡ linear); bpchar trailing-space pin
(storage-time trimming keeps both paths consistent; PG deviation documented);
bytea/bool/timestamp end-to-end; regression guard that numeric-vs-text and
enum sets still decline; `TestBuildSubPlanHashClassification` rows for the
big lane.

---

## 3. DML-sublink lowering

### 3.1 Root cause: one missing walker case, nothing else

`Plan()` already runs `lowerSubPlanParams` for **every** statement kind
(planner.go:94) — the gap is entirely inside host discovery:
`walkPlanExprs` (unnest.go:970-1147) has cases for ~20 node kinds but
**none for `*Update` / `*Delete` / `*Insert` / `*Merge`**, so a DML root
falls through the type switch without descending into `Update.Child`. Note
`planUpdate` hangs the WHERE as a `Filter` **on its fallback arm only**
(planner.go:8553-8560); the `planIndexScanFromWhere` arm (:8541-8552)
absorbs the predicate into an `*IndexScan` probe key, so DML host discovery
must handle both `Update.Child` shapes. Even a
CTE-wrapped DML is missed: the `*CTEDMLPrefix` case recurses into the DML
node, which then no-ops. `unnestSubqueriesInPlan` is likewise DML-blind
(called only from `planSelect`), so DML WHERE sublinks are neither unnested
nor lowered — the ledger row's full extent.

The operand half of the original ledger row is **already resolved** (R2-2's
S4b work: both lowering phases traverse `InExpr.Operand`/`List`,
subplan_lower.go:177-188/:378-393); only the DML half remains.

### 3.2 Why the executor needs zero new plumbing

The DML operators evaluate their predicate through the shared `evalExpr`
against the target scan row (operators_storage.go:3721 etc.); a lowered
sublink is driven purely by `x.ParParam`/`x.Args`, and `bindSubPlanParams`
(expr.go:6716-6729) binds Args against whatever row the eval site was
handed — for DML that is exactly the host row a lowered
`ColumnRef` Arg expects. Today `ParParam` is simply empty, so all three eval
sites take the correct-but-slower full-row scoped-cache path
(stack push at expr.go:6970; full-row keys at :6771/:7055).

### 3.3 Scope decision: first cut = Child-predicate DML

Add DML cases to the host-discovery walk that recurse into the child
plan(s): `Update.Child`, `Delete.Child`, `Insert.Source` (the WHERE-bearing
`Filter` under `Child` is then discovered by the existing machinery).
**Deliberately excluded from this cut** (stay on the stack path,
ledger-recorded):

- `Update.FromPred` / `Delete.UsingPred` loose predicates and the
  `FromScans`/`UsingScans` combined-row shapes — lowered Args must carry
  combined-schema `SourceTableIdx` values; higher risk, no measured
  workload;
- `Merge` clauses and `OnConflict` expressions — same argument.

**Implementation mandate: a dedicated wrapper, NOT an extension of the
shared walker.** `walkPlanExprs` has **ten non-test call sites plus a
cross-package export** (`walk_export.go:10` → executor/subplan.go:162).
Most are detection predicates that could absorb extra descent safely
(over-detection just bails), but **two cannot**:

- `bushy.go:1728` (`remapOuterRefsInSubplan`) — a **rewriter**: it mutates
  `OuterColumnRef.Index`, so newly-visited DML-child expressions would be
  remapped.
- `unnest.go:704` (`collectUnnestParams`) — a **harvester**: it would
  collect equijoin pairs from inside a CTE-DML body.

Since DML nodes are **already reachable** from the shared walker via
`*CTEDMLPrefix` (unnest.go:1124-1126), adding DML cases in place would
newly expose CTE-DML bodies to both. `planContainsLateralJoin` moreover
documents a **paired-coverage contract** with the lowering traversal
("over-detection is safe (a bail), under-detection is impossible … because
that traversal bails on every node kind `walkPlanExprs` does not also
cover", subplan_lower.go:~273-276) — widening perturbs both sides of an
argument that currently holds by construction.

Therefore R3-5 adds a **host-discovery-local** walker (e.g.
`walkPlanExprsIncludingDML`) that handles the DML cases and delegates
everything else to `walkPlanExprs`, and changes **only** the
`lowerSubPlanParams` call site (subplan_lower.go:117). Every other caller
keeps today's exact behaviour; the paired-coverage contract is re-argued in
a comment at the new wrapper; and a test pins that `walkPlanExprs` itself
still does not descend into `Update.Child` (protecting bushy.go:1728).

Halloween/EPQ safety: lowering changes only *how* the outer value reaches
the subplan (param slot vs. stack push), never *when* it runs or which
snapshot it sees; Args are re-bound per row on every call, including the
EPQ recheck's re-fetched row. Semantics-preserving by construction, pinned
by test anyway.

`SubplanLowerMismatches()` note: extending discovery legitimately flips
some DML sublinks' `IsNonCorrelated`; no DML test may assert the mismatch
counter stays zero.

### 3.4 Tests

- Planner shape: `UPDATE … WHERE EXISTS(correlated)` and `DELETE … WHERE …
  IN (SELECT correlated)` produce `len(ParParam)==1`, host-`ColumnRef`
  Args, inner plan 0 `OuterColumnRef` / ≥1 `ExecParamRef` (assertions
  mirrored from subplan_lower_test.go:117-136 — currently SELECT-only).
- Executor equivalence (dual-path): row effects + RETURNING for correlated
  EXISTS / NOT EXISTS / IN / NOT IN / scalar, each also run with
  `GOOPG_SUBPLAN_RESCAN=off` and `GOOPG_HASHED_SUBPLAN=off`; the
  self-referencing `UPDATE t … WHERE EXISTS(SELECT … FROM t)` Halloween
  case; one `UPDATE … FROM` case pinned as *staying on the stack path*
  (the exclusion guard).
- There is currently **no** DML+correlated-sublink test in the repo — this
  stage creates the class.

---

## 4. LEFT+residual NLI hazard (upgrade: audit → live-bug fix)

### 4.1 Audit verdict: the guard is shape-contingent, not structural

The ledger's premise "LEFT+residual currently declines NLI" holds **only for
Q13's shape** (inner-only ON residual → pushed into a `Filter{inner}` wrapper
→ `pickInnerSide` declines the non-SeqScan inner). Two planner paths attach a
residual `Predicate` to a **LEFT** NLI today, ungated by join type:

- **Q7-style leftover retention** (nl_index_join.go:601-609 → :723-725 →
  :741): a LEFT join whose ON clause carries a *cross-relation* non-equi
  conjunct (classified `sideMixed` by `classifyConjunctSide`, kept on
  `jn.Predicate` by the LEFT split in planner.go:1936-1989) reaches
  `tryBuildNLI` with a bare-SeqScan inner → `LEFT + Predicate`.
- **Q19 OR-factoring** (nl_index_join.go:380-386): a LEFT ON that is a pure
  OR-of-ANDs with a common equi conjunct.

The first route depends on `j.LeftKey`/`RightKey` having been pre-attached:
`extractEquiKeys` (nl_index_join.go:1084-1092) inspects `j.Predicate` only
when it is a *bare* top-level `OpEq`, so an AND-shaped ON residual reaches
the leftover code solely via the `LeftKey != nil` arm that
planner.go:2050-2055 populates for LEFT. Verified in-process:
`cust LEFT JOIN ordr ON c_key = o_key AND o_total > c_bal` yields
`NLI{Type: JoinTypeLeft, Predicate != nil}`.

So the hazard is live, merely un-exercised by TPC-H's 22 queries.

### 4.2 The two executor defects (operators_nljoin.go)

- **Defect 1 — match flag set before the residual is evaluated.**
  `leftJoinEmitted = true` fires for every probe-produced inner row
  (:194) *before* `evalPredicateSlot` (:197). Anti resets it on predicate
  failure (:206-208); **LEFT does not**. If every probe candidate fails the
  residual, the null-pad fallback guard (:165) sees `leftJoinEmitted==true`
  → the preserved outer row is **silently dropped**. This is the exact bug
  class fixed for the hash join in M0119-0004
  (operators_join_agg.go:991-998 sets the match flag only after the
  predicate passes) — the NLI operator never received that fix.
- **Defect 2 — residual evaluated against the null-padded row.** The
  zero-candidate fallback (:165-174) sets the inner slot to `nullInner` and
  then gates emission on `evalPredicateSlot()`. A residual referencing an
  inner column evaluates NULL→false → outer row dropped. PG requires the
  padded row to emit **unconditionally**: the NLI `Predicate` is populated
  from the JOIN **ON** residual (join-condition semantics), not a post-pad
  WHERE filter. The hash path already does this correctly
  (operators_join_agg.go:1023-1028 emits the padded row without
  re-evaluating the predicate).

  Worse in practice than the ledger implied: `nliConsumedByProbe`
  (nl_index_join.go:815-833) matches the probe key by **pointer identity**
  (`bin.Right == k`) against a rebuilt `keys` slice, which generally fails
  — so the retained residual typically still *contains the equi probe-key
  conjunct*. The null-padded row then fails on that conjunct alone, meaning
  **every** unmatched outer row is dropped regardless of whether the ON
  residual references an inner column. Confirmed live: the NLI path
  returned 1 of 4 rows where the hash path returned 4.

### 4.3 Fix design

Executor-only, mirroring the hash operator's proven discipline:

1. Track "matched" ≙ "some inner candidate passed the full predicate": move
   the flag set to after the `!ok → continue` branch for the LEFT/INNER emit
   path (LEFT adopts Anti's reset discipline; or a separate bool — decided
   at implementation by whichever keeps the Anti semantics untouched).
2. Zero-passing-match fallback: emit `outer ++ nullInner` unconditionally —
   delete the `evalPredicateSlot` call and its gating in the LEFT fallback.
3. (Consequence of 2) the predicate is never evaluated on the padded row.

Planner side: **no change required for correctness** once the executor is
fixed. The S6 Filter-inner unwrap keeps its LEFT exclusion for the
documented **cost** reason (Q13's 150K×1.5M NOT-LIKE probe blowup) — that
is a separate, still-valid decision; re-record it as cost-motivated rather
than correctness-motivated. Memoize's LEFT allowance (memoize.go:79) rides
on top of the fixed operator and needs no change.

### 4.4 Pinning tests

- T1 (defect 1): LEFT NLI, outer key matches ≥1 inner candidate, residual
  rejects all → exactly one null-padded row (canonical Q13 NOT-LIKE shape
  built in its cross-relation form so the residual lands on
  `nli.Predicate`).
- T2 (defect 2): LEFT NLI, outer key matches zero inner rows,
  `Predicate != nil` referencing an inner column → null-padded row emitted.
- Both modelled on `leftjoin_hash_residual_dropped_row_test.go` but driving
  the NLI operator (indexed inner). There is currently **no** NLI LEFT+
  residual test anywhere.
- Planner pin: a cross-relation LEFT ON residual produces `LEFT+Predicate`
  NLI (the §4.1 leak path) and now returns PG-correct results end-to-end.

---

## 5. Composite-equijoin EXISTS decorrelation

### 5.1 Current state

`unnestExistsExpr` bails at `len(params) > 1` (unnest.go:3610; NOT EXISTS
shares the function). History: the pre-S1c code used only `params[0]` as the
hash key and silently dropped extra pairs (over-matching wrong results);
S1c made it bail. Correlated IN has the same guard indirectly
(`correlatedInOperandSafeToUnnest`, unnest.go:2361).

### 5.2 Why the fix is small

The machinery already exists end-to-end:

- **Template:** the scalar path already does multi-param
  (unnest.go:2012-2025, :2040-2042) — but note what it actually does: it
  ANDs **every** pair, `params[0]` included, onto `join.Predicate` and
  *additionally* sets `LeftKey`/`RightKey` from pair 0. Note also the
  coordinate asymmetry the EXISTS port must reproduce: predicate
  `innerKeyExprs[i].Index = outerWidth + i` (merged coordinates) vs
  `RightKey.Index = 0` (inner-child-local). Copying the template without
  noticing this produces silently mis-indexed residuals.
- **Executor:** the lazy hash semi/anti walks every hash-bucket match and
  applies the full `plan.Predicate` per match
  (operators_join_agg.go:1097-1124) — an equi residual is evaluated exactly
  like Q21's `<>` residual.
- **NLI:** `collectCrossSideEquiKeys` (nl_index_join.go:841-917) already
  harvests equi conjuncts from **both** `LeftKey/RightKey` and
  `Predicate` conjuncts into a composite probe key when a covering index
  exists; uncovered pairs stay as executor-enforced residuals.
- **predp:** `whereEligibleForPreDPUnnest` has no param-count assumption —
  composite EXISTS becomes S5a-eligible automatically.

Change: replace the bail with the scalar path's technique — keep `params[0]`
as `LeftKey/RightKey`, AND `params[1..]` outer=inner equalities onto the
join predicate alongside the lifted non-equi residuals.

### 5.3 Guards and caveats

- **The historical failure mode to test, not fear:** the old bail rationale
  worried an equi residual could be "extracted as a competing probe key and
  the pair silently dropped." The NLI harvest is *correct* when a covering
  composite index consumes all pairs, and leaves uncovered pairs as
  residuals otherwise — the design pins this with an indexed-vs-unindexed
  dual test rather than keeping the bail.
- **Project-strip index math:** the strip at unnest.go:3696-3703 is
  **unconditional** — its comment claims a "check whether RightKey column
  index is accessible in the projected output" that is not implemented, so
  this is a stale-comment/dead-guard defect today, not merely a widening
  caveat. The composite fix must *write* that guard (validate every
  `params[i].SubCol.Index` against the stripped schema, else refuse the
  strip), not verify an existing one.
- **NOT EXISTS NULL semantics are safe with a plain anti join** (no
  `NullAware`): EXISTS is a pure existence test — an equality with NULL on
  either side is UNKNOWN, never TRUE, so a NULL key is a non-match, not a
  NOT-IN-style poison. NULL probe key → `evalHashKey` ok=false → Anti keeps
  the row (correct); NULL in a residual pair → candidate fails → correct.
- **Correlated IN stays bailed this round** (operand-safety must be
  re-proven per pair; smaller follow-up, ledger-recorded).
- **Row-comparison IN `(a,b) IN (SELECT …)`** is a separate evaluator path
  (`evalRowConstructorInExpr`, excluded from lowering at
  subplan_lower.go:93-94) — orthogonal, untouched.

### 5.4 Tests (matrix rows M23–M27 + executor pins)

- M23 composite EXISTS both keys non-NULL (guards the historical
  over-match); M24 composite NOT EXISTS with NULL in one key column (plain-
  anti NULL-as-non-match); M25 first-key match/second-key mismatch (the
  exact original bug); M26 composite + non-equi residual coexisting; M27
  covering-composite-index vs no-index identical results (guards the
  competing-probe-key hazard).
- Executor-level composite-key semi and anti NLI emit-once/NULL tests
  alongside `nl_semi_anti_join_test.go`.

---

## 6. Round staging

One commit+push per stage on `planner-kaizen3`; round-1/2 gate discipline
(units / spotcheck Q12=2 & Q13=33 / plan-gate review→recapture when intended
/ race-gate on executor-shared-state changes / pre-commit pgbench smoke);
IMPLEMENTATION-TODO.md gains a Round 3 part; every deliberate exclusion gets
a ledger row. All server starts under the cgroup wrapper
(`scripts/csq-bench-server.sh` / `scripts/goopg-test-run.sh`), which since
R2-0 refuses `memory.high < GOMEMLIMIT`.

Ordering rationale: §1.4 resolves item 1 as World A (no bug), so its stage
is documentation-and-pins. The only remaining **live wrong-results** items
are item 4 and §2.3's string-normalisation hazard, so both precede it.

| # | stage | scope | named tripwires |
|---|---|---|---|
| R3-0 | this design chapter + TODO round-3 part | docs only | — |
| R3-1 | item 4: NLI LEFT executor fix (defects 1+2) + T1/T2 + planner leak pin; S6-unwrap LEFT exclusion re-recorded as cost-motivated | race-gate; Q13 spotcheck (hash path must stay untouched); plan-gate zero-diff expected — **but the SF1 set has no LEFT+cross-relation-residual query, so plan-gate green does not exercise this fix; the end-to-end row-count assertions do** |
| R3-3 | item 2: string-shape decline (the live hazard, first) + big-numeric widening + stale-comment fix + fixture growth | units gate table; plan-gate zero-diff expected |
| R3-2 | item 1: close World A (falsification evidence + two regression pins) | spotcheck; the SF1 probe pair |
| R3-4 | item 5: composite EXISTS/NOT EXISTS + M23–M27 + NLI composite pins + the Project-strip guard | Q4/Q21/Q22 spotcheck (semi/anti shapes must not move); plan-gate zero-diff expected (no TPC-H query has a composite-correlated EXISTS); **explicit coordinate-space assertion** (Predicate merged vs RightKey child-local) |
| R3-5 | item 3: DML-sublink lowering per §3 | dual-path DML tests; race-gate if executor state touched; pin that `walkPlanExprs` still does not descend into `Update.Child` |
| R3-6 | FINAL: full gate sweep + capped SF1 spotcheck-plus (the five tripwire queries) + round-3 report + ledger row resolutions | all |

Plan-stability expectation: **zero TPC-H plan changes in every stage** —
items 1/2/3 do not touch TPC-H-visible plans; item 4 is executor-only; item
5's shape does not occur in TPC-H (Q21 is one equijoin + one non-equi
residual). Any plan-gate diff is a bug in the stage that produced it.
