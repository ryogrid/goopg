# C-05 (P1-18) — outer/semi/anti joinrel sizing: the `calc_joinrel_size_estimate` jointype switch

Status: accepted 2026-09-06. Implements TODO_ALL.md C-05 (take3 08 §4
P1-18, §6.2). After C-03 (jointype on paths) and C-04a (LEFT admission).
Files: `internal/optimizer/joinrelsize.go` (the switch),
`joinselectivity.go` (semi/anti clause selectivity), `cardinality.go`
(shared `eqjoinsel_semi` core), `joinsearchlevel.go:makeJoinRel` (the
rel-level publication), and their tests.

## 1. Objective

The PG-shaped join search sizes every joinrel with INNER-join maths
(`calcJoinrelSize`: `outer × inner × fkselec × jselec`) and publishes
every joinrel's width as the concatenation of its inputs. C-04a bolted a
LEFT/RIGHT row floor on top (`applyOuterJoinRowFloor`) and C-03c left
SEMI/ANTI rel-level sizing at the union on purpose ("over-wide beats
under-wide; C-05 installs the real switch"). This item installs the real
switch: PG's `calc_joinrel_size_estimate` jointype table
(`postgres/src/backend/optimizer/path/costsize.c:5501-5637`), the
jointype-aware clause selectivity it depends on (`eqjoinsel_semi`,
`neqjoinsel`'s semi arm, `selfuncs.c:2642`, `:2830`), the SEMI/ANTI arm
of `get_foreign_key_join_selectivity` (`costsize.c:5694-5697`,
`:5811-5827`), and the left-only rel-level publication for SEMI/ANTI so
that a parent joinrel is sized on what its child actually emits.

## 2. Oracle, verbatim shape

`calc_joinrel_size_estimate` (costsize.c:5501):

```
fkselec = get_foreign_key_join_selectivity(outer, inner, sjinfo, &restrictlist)
if IS_OUTER_JOIN(jointype):           # LEFT, FULL, RIGHT, ANTI
    jselec = clauselist_selectivity(joinquals,  jointype, sjinfo)
    pselec = clauselist_selectivity(pushedquals, jointype, sjinfo)
else:                                 # INNER, SEMI
    jselec = clauselist_selectivity(restrictlist, jointype, sjinfo)
switch jointype:
    INNER: nrows = o * i * fkselec * jselec
    LEFT:  nrows = max(o * i * fkselec * jselec, o) * pselec
    FULL:  nrows = max(o * i * fkselec * jselec, o, i) * pselec
    SEMI:  nrows = o * fkselec * jselec
    ANTI:  nrows = o * (1 - fkselec * jselec) * pselec
return clamp_row_est(nrows)
```

`RINFO_IS_PUSHED_DOWN(rinfo, joinrelids)` (pathnodes.h) is
`rinfo->is_pushed_down || !bms_is_subset(rinfo->required_relids,
joinrelids)`: a clause is "pushed down" when it did not come from this
outer join's ON list (a WHERE clause evaluated at or above the join).

`eqjoinsel` (selfuncs.c:2280) dispatches on jointype: INNER/LEFT/FULL →
`eqjoinsel_inner`; SEMI/ANTI → `eqjoinsel_semi` with `inner_rel =
find_join_input_rel(sjinfo->min_righthand)` and the variables SWAPPED
when the clause was written RHS-first, so that `vardata1` is always the
outer (preserved) side. `eqjoinsel_semi` (:2642) clamps `nd2` to
`vardata2->rel->rows` and to `inner_rel->rows`, takes the MCV arm when
both sides have MCV lists (`matchfreq1 + uncertainfrac * uncertain`), and
otherwise `(1 - nullfrac1)` when `nd1 <= nd2`, `(nd2/nd1)(1 - nullfrac1)`
when `nd1 > nd2`, `0.5 (1 - nullfrac1)` when either nd is a default.
`neqjoinsel` (:2830) returns `1 - nullfrac(outer var)` for SEMI/ANTI.

`get_foreign_key_join_selectivity`'s SEMI/ANTI arm (:5694-5697,
:5811-5827): the FK is used only when the referenced table is the WHOLE
inner (a singleton) — "if the referenced rel is on the inside, then all
outer rows must have matches in the referenced table" — and the
selectivity is then `ref_rel->rows / ref_rel->tuples` (the fraction of
referenced rows that survive their own restrictions), not
`1 / ref_tuples`. Any other configuration punts the FK and leaves the
clauses to `eqjoinsel_semi`.

## 3. What goopg does today (delete/replace list)

1. `calcJoinrelSize` (joinrelsize.go:143) has one arm — INNER — and
   takes no sjinfo. Its two clamps (key-implied `rowsBound`, the
   `max(l,r)` all-default cap) are goopg's stand-ins for `fkselec` on a
   schema with no declared FKs and stay exactly where they are.
2. `applyOuterJoinRowFloor` (joinrelsize.go:118) is the LEFT/RIGHT
   `max(…, preserved)` clause of the switch, applied AFTER `clampRowEst`.
   Folded into the switch and deleted. Numerically identical on every
   LEFT joinrel the search builds today (`max(round(x), o) =
   round(max(x, o))` for integer `o ≥ 1`), so the production LEFT arm
   is a zero-drift move — and the gate asserts that.
3. `makeJoinRel` (joinsearchlevel.go:547) sums `NCols`, `AvgVarBytes`,
   `ColVarBytes` over both inputs regardless of jointype; the width
   from `sizeJoinRel` is `outer.Width + inner.Width` likewise.
4. `joinClauseSelectivityExt` (joinselectivity.go:360) has no jointype:
   every equality is `eqjoinsel_inner`.
5. `superkeyJoinSelectivity` (joinrelsize.go) has no jointype: a SEMI
   join over a proven key would be charged `1/rawTuples` and then have
   its `o × i` product... there is no product for SEMI, so today it
   would simply be wrong.

## 4. The port

### 4.1 Jointype and orientation

`sizeJoinRel(outer, inner, clauses, sjinfo)` → `calcJoinrelSize(cat,
outer, inner, clauses, sjinfo)`. `sjinfo == nil` is INNER. `makeJoinRel`
has already applied `join_is_legal`'s `reversed` swap, so `outer` covers
`sjinfo.MinLefthand` (the SJI's LHS) and `inner` covers `MinRighthand`.
goopg keeps RIGHT as its own jointype where PG has already commuted it
to LEFT (`outerJoinRowFloor`, cardinality.go, records the same
convention for the plan-node estimator): a RIGHT SJI's LHS is its
NULLABLE side, so the preserved input is `inner`. The switch therefore
has six arms, not five:

| jointype | rows |
|---|---|
| INNER | `clampInner(o·i·fk·j)` |
| LEFT | `max(clampInner(o·i·fk·j), o) · p` |
| RIGHT | `max(clampInner(o·i·fk·j), i) · p` |
| FULL | `max(clampInner(o·i·fk·j), o, i) · p` |
| SEMI | `o · fk · j` |
| ANTI | `o · (1 − fk·j) · p` |

`clampInner` is the existing pair of clamps (key-implied bound, then the
all-default `max(l,r)` cap) — they bound the INNER product, which is the
term they were designed against, and PG's outer-join floor is then
applied on top of the bounded product exactly as PG applies it on top of
`fkselec` (the clamps ARE goopg's fkselec, cost-model/14 §2). SEMI/ANTI
never form the product and never see the clamps; their bound
(`rows ≤ o`) is structural in the formula. `clamp_row_est` last.

FULL is sized even though `jointypeForDirection` declines both of its
directions at path generation (C-03c, ledger `C-03c
FULL-join-search-decline`): the rel is built and sized before paths are
tried, and an honest size for an empty-pathlist rel costs nothing while a
missing arm would be one more place to fix when FULL is admitted. There
is no seventh arm: an unrecognised jointype is PG's `elog(ERROR)`; goopg
returns the INNER product with the clamps — the pre-C-05 behaviour, and
fail-closed in the same direction the C-04a floor was.

### 4.2 `pselec` inside the search is 1.0 — by construction, not by omission

The search never holds a pushed-down clause. `tryJoinSearch`
(joinsearchseam.go:308-338) holds every WHERE conjunct whose relids
reach an admitted outer link's nullable side ABOVE the searched tree
(C-04a §3.5, `check_outerjoin_delay`'s rule), and every clause that
does enter `restrictInfoList` is either a WHERE conjunct proven not to
touch a nullable side, an ON conjunct of an admitted link, or a
`reconsider_outer_join_clauses` constant that becomes a leaf-local
filter. `buildJoinRelRestrictList` further restricts to `relids ⊆
joinrelids`, so the second disjunct of `RINFO_IS_PUSHED_DOWN` is
structurally false too. Every clause the sizer sees is a joinqual;
`pselec ≡ 1`. The switch is written with `pselec` in its arms as a
named factor so the arms read as PG's, and the factor is derived from
one function, `pushedDownSelectivity`, that today partitions nothing
and says why. Resume condition, ledger `C-05 pselec-held-above`: when a
later slice distributes delayed WHERE quals INTO the search (C-02's
`delayedAboveOJ` placement, C-04c), `restrictInfo` grows
`isPushedDown` and the partition becomes live — the arm already
consumes it.

### 4.3 Jointype-aware clause selectivity (`clauselist_selectivity(…, jointype, sjinfo)`)

`joinClauseSelectivityExt(ri)` becomes `joinClauseSelectivityExt(ri,
jt, outer, inner)`. INNER/LEFT/RIGHT/FULL keep every existing arm.
SEMI/ANTI:

- `=`: `eqJoinSelectivitySemi(v1, v2, inner.Rows)` — `eqjoinsel_semi`
  with `v1` the operand on the OUTER side (the swap `eqjoinsel` does
  through `get_join_variables`' `join_is_reversed`; goopg's
  `leftKey`/`rightKey` split is canonical, not oriented, so the sizer
  orients by `leftRelids ⊆ outer.Relids`). Both nd2 clamps are ported —
  `vardata2->rel->rows` is `joinVarStats.rows` (the base rel's
  post-filter count, already carried for B-16) and `inner_rel->rows`
  is `inner.Rows`. The MCV arm and the nd heuristic are ONE function,
  `eqjoinselSemiCore`, shared with the plan-node estimator's
  `semiPairMatchFraction` (cardinality.go) — the two sibling paths
  (hard-won rule #2) now compute the same number from the same inputs.
  The plan-node twin keeps its historical divergence of clamping nd2 to
  the inner INPUT's rows only (it has no `rel->rows`); the search-side
  applies both, as upstream does.
- `<>`: `1 − nullfrac1` (neqjoinsel's semi arm). Reported as "not
  default" when the outer var resolved to statistics.
- ordering comparisons and unhandled clauses: the same selfuncs.h
  constants as INNER (upstream's `scalarltjoinsel` has no jointype).

`isdefault` semantics for the fallback cap: unchanged for the product
arms. SEMI/ANTI never consult the cap.

### 4.4 `fkselec` for SEMI/ANTI

`superkeyJoinSelectivity(cat, outer, inner, clauses, jt)`: for
SEMI/ANTI only DECLARED foreign keys whose referenced (parent) relation
is exactly the singleton `inner` are applied, with selectivity
`parent.Rows / parent.rawTuples` (costsize.c:5811-5827); the FK arm is
otherwise punted. The UNIQUE-index extension (cost-model/14 §2: a
composite unique index accepted as the evidence PG accepts a composite
FK for) is NOT extended to SEMI/ANTI: a unique key on the inner says
each outer row matches at most one inner row, which bounds an INNER
product, but says nothing about the FRACTION of outer rows that match —
that is the FK's referential guarantee and only the FK has it.
`eqjoinsel_semi`'s `nd1 <= nd2 → 1 − nullfrac1` already prices a unique
inner correctly. The `rowsBound` clamp is likewise not produced for
SEMI/ANTI (`o` is already the bound).

### 4.5 Rel-level publication: SEMI/ANTI emit the LHS only

`joinPublishesInner(sjinfo) bool` — false for SEMI/ANTI, true otherwise
— is the single meeting point. `calcJoinrelSize` uses it for `Width`;
`makeJoinRel` uses it for `NCols`, `AvgVarBytes`, `ColVarBytes` (the
concatenation of the two inputs when true, `rel1`'s figures alone when
false). C-03c's objection — "a relset-keyed singleton cannot hold a
jointype; different pairs spanning one relset can match different SJIs,
so a jointype-dependent width would be arrival-order dependent" — is
answered by a relset invariant rather than by a carrier: whether a base
relation's columns are published is a property of the RELSET, because
`joinIsLegal` (joinsearchlevel.go:198, PG joinrels.c:350) admits a
relset containing any of a SEMI/ANTI SJI's `MinRighthand` only if it is
inside the RHS entirely or contains the SJI's LHS as well (the RHS
cannot be joined outward before the semijoin is performed; the
unique-ified exception, `create_unique_path`, does not exist in goopg).
So for a relset that contains both hands the semijoin HAS been
performed somewhere inside it, on every pair that spans it, and the RHS
columns are absent from every route's publication:

```
{a SEMI b} ⋈ c :  route ({a,b},{c}) → SEMI inside {a,b} → cols(a) ; + cols(c)
                  route ({a,c},{b}) → SEMI at the pair   → cols(a,c)
                  route ({a},{b,c}) → illegal ({b,c} pairs the RHS outward)
```

Rows agree across routes for the same reason PG's do (`o·sel_semi·c·sel`
either way), which is the rows-once discipline of leftdeep-joins 04 §2
and PG's own caveat at costsize.c:5411-5421.

### 4.6 How it reaches the parent

Through `RelOptInfo` and nothing else: the parent's `calcJoinrelSize`
reads `outer.Rows`/`inner.Rows`, its width sums `Width`, and
`makeJoinRel` sums `NCols`/`AvgVarBytes`/`ColVarBytes`. A SEMI child
therefore contributes `≤ o` rows and its LHS width, and the parent's
hash sizing (`hashsize.Choose` via `relNCols`, `entrywidth.go`) sees the
same schema `createPlanNode`'s SEMI/ANTI arms publish (C-03c
`publishedSchema`/`publishedLayout`). The costing over-estimate C-03c
recorded is closed; no plan-level code changes.

### 4.7 What does NOT change

- The plan-node estimator `estimateJoin` (cardinality.go:583) already
  carries the switch for the syntactic tree; only its `eqjoinsel_semi`
  core is factored to be shared. `outerJoinRowFloor` stays.
- The `problemPairsOuterWithDerived` firewall (relfromjoinlist.go:609)
  stays; its resume condition is B-06 (CTE-output statistics), not
  this item (§6).
- FULL path decline (C-03c), SEMI/ANTI nestloop-only (C-03b), and the
  fact that SEMI/ANTI SJIs do not reach the search today (they are
  pinned above it, `runJoinSearchBelowPinned`, C-04 DESIGN §3.6) are
  unchanged. The SEMI/ANTI arms are live code with no production caller
  until a slice admits semi/anti links; every arm is forced through by
  test.

## 5. Acceptance

1. Forced-shape units on `calcJoinrelSize` with a hand-built
   statistics context, one per arm (INNER, LEFT, RIGHT, FULL, SEMI,
   ANTI, and the nil-sjinfo INNER), every expectation a named constant
   derived from the same inputs (`o`, `i`, `nd`, `nullfrac`) — never a
   literal row count. SEMI/ANTI additionally with a declared FK on the
   singleton inner (`parent.Rows/parent.rawTuples`), with a UNIQUE
   index only (NOT applied), and with a two-relation inner (punt).
   `eqJoinSelectivitySemi`: nd arms, the `inner.Rows` and `rel.rows`
   clamps, the MCV arm, and parity with `semiPairMatchFraction` through
   the shared core. Rel-level: `makeJoinRel` through the real
   `searchJoinRelBuilder` publishes LHS-only `Width`/`NCols`/
   `AvgVarBytes`/`ColVarBytes` for SEMI and ANTI and the concatenation
   for LEFT/INNER; a three-relation case checks the parent of a SEMI
   child sums the child's narrowed figures (§4.6). `TestOuterJoinRowFloor`
   is replaced by the switch test.
2. `go build ./...`, `go vet ./internal/optimizer/`, `go test
   ./internal/optimizer/ ./internal/executor/` (no `-count=1`).
3. Zero-drift on the production shapes: TPC-H 24/24 values via
   `tpch-runner -digest`/`-diff`; serial plan capture diffed against
   `plan_snapshots/c04a-fixed-20260906.txt` — the expected diff is
   EMPTY (§3 item 2: the LEFT arm is numerically the C-04a floor; no
   SEMI/ANTI/RIGHT/FULL joinrel is searchable today). Any plan that
   moves is timed, same-session A/B against the pre-change binary.
   `make plan-gate` + `MODE=costs`.
4. TPC-DS SF0.5 full sweep `PASS=95 MISMATCH=0 CKMISMATCH=0 TIMEOUT=0`
   and plan-shape census against the C-04a pin.
5. Q78: with the firewall in place its decline still fires (trace
   `seam-decline reason=outer-over-derived`) and the estimates are
   unchanged. To answer "can the firewall come down", a SCRATCH binary
   with the decline disabled is planned (EXPLAIN only) under C-05: the
   LEFT arm gives `max(1·1·sel, 1) = 1` on `rows=1` CTE leaves, so the
   epsilon-cost shape is expected to reproduce unchanged. Reported, not
   committed.

## 6. Risks and what this does not claim

- C-05 cannot repair Q78. The lie is in the LEAF rows (three CTE
  outputs at `rows=1`, rowest A3/A4, B-06), and every arm of the switch
  is monotone in `o` and `i`. The firewall's resume condition stays B-06.
- A3 (`LEFT JOIN … WHERE x IS NULL` → ANTI declined, `reduce_outer_joins.go`)
  lives at the statement level; the resulting ANTI is pinned above the
  search and sized by `estimateJoin`. No interaction in the searched
  tree by construction; the plan census in §5.3/§5.4 is the measurement.
- Ledger rows: `C-05 pselec-held-above` (§4.2), `C-05
  semi-anti-unique-not-fk` (§4.4, a deliberate non-extension, recorded
  so nobody "completes" it), `C-05 plan-node-semi-nd2-rel-rows` (the
  plan-node twin's one remaining divergence, §4.3).
