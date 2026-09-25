# M0142-0008a-3i-route-a step 1 — retain the sublink parse tree, and be able to classify it

Status: LANDED 2026-09-20 — both steps. Step 1 inert by construction
(zero production readers); step 2 is the flattening splice, movement
measured NONE on the SF0.25 corpus (see §6).
Kind: impl
Parent: M0142-0008a-3
Movement: none

## 1. What step 1 is for

`m0142-0008a-3i-lateral-route-recon.md` located the route defect: goopg plans
every EXISTS/IN body eagerly, at expression-resolution time
(`planner.go:1499` → `planExistsExpr`/`planInExpr` → `planSelectWithParent`),
and only then unnests (`:1539`) and searches (`:1540`). PG inverts that —
`pull_up_sublinks` (`prepjointree.c:468`) and `pull_up_subqueries` (`:1083`)
run while the body is still an unplanned `Query`, and `SS_process_sublinks`
(`subselect.c:2026`, reached at `planner.c:1328`) plans only what pull-up
refused.

Two things were missing before goopg could do the same, and neither is the
transform itself:

1. **The parse tree was gone.** `ExistsExpr` and `InExpr` carried `Plan Node`
   and nothing else; `x.Subquery` was consumed by `planSelectWithParent` and
   dropped. There was nothing left to flatten.
2. **Nothing could say whether a body is flattenable at all.** PG's gate is
   `is_simple_subquery` (`prepjointree.c:1807`), and goopg had no analogue.

Step 1 lands exactly those two, and nothing else. Both are inert: no
production code reads either. That is the same posture
`pathkeysCountContainedIn` (M0141-S7) and `costIncrementalSort` (M0141-S7b)
landed under, and it is deliberate — the splice that consumes them is a
change to *when a query is planned*, and it should be attempted against
prerequisites that are already proven present and already proven harmless.

## 2. Retaining the parse tree

`ExistsExpr.Subquery` and `InExpr.Subquery` (`plan.go`), assigned at the two
resolver sites that build them (`planner.go:15306`, `:15338`).

**The retention test caught a real sibling-drift bug, which is why it exists
in the pointer-comparing form it does.** `FoldConstants` (`foldconst.go:69`)
REBUILDS an `InExpr` field by field, so the newly added field was silently
dropped between the resolver and every later stage — the EXISTS arm passed
while the IN arm failed. Three sites copy a sublink and must carry the field:

| site | what it does | carries `Subquery`? |
|---|---|---|
| `foldconst.go:69` | rebuilds the node after folding | **yes** (was the bug) |
| `planner.go:16834` | remaps column refs to a new schema | **yes** |
| `planner.go:17072` | shifts column refs by a delta | **yes** |
| `exists_to_any.go:384` | REWRITES `EXISTS(...)` into `x IN (SELECT ...)` | **no, deliberately** |

The fourth is not a copy. It builds a different query over a re-projected
body, so the original parse tree no longer describes the plan attached to it;
leaving `Subquery` nil there makes the body read as "not pull-up-able", which
is the fail-closed answer and the correct one.

`TestSublinkParseTreeIsRetained` compares POINTERS, not shapes — a re-parse
producing an equal-looking tree would pass a shape comparison and prove
nothing. It was verified to FAIL with the two resolver assignments removed.

## 3. `sublinkBodyIsSimple` — the `is_simple_subquery` port

`internal/optimizer/sublinkpullup.go`. Every refusal is one of upstream's, in
upstream's order: set operations, CTEs, GROUP BY / grouping sets / HAVING,
ORDER BY, DISTINCT (goopg splits plain and `DISTINCT ON` across two fields
where upstream has one clause), LIMIT / OFFSET, `FOR UPDATE`/`FOR SHARE`, and
aggregates / window functions.

goopg's `parser.SelectStmt` carries no `hasAggs`/`hasWindowFuncs` summary
flags, so those are derived from the only two places a sublink body can hold
one — its target list and its WHERE — using the existing `walkExpr`
(`planner.go:9597`) over the existing `isAggregateFuncName` (`:9662`). The
name table is referenced, never copied: a second copy here is precisely the
sibling drift this repository's own rule warns about, and §2 shows the rule
earning its keep in the same change.

Two goopg-only refusals, both stated as such rather than smuggled in as
upstream: a `VALUES` body and a `FROM`-less body are refused because they have
no FROM items to splice, so "simple" would be true but vacuous.

### 3.1 What this port does NOT claim

Recorded here and in the ledger rather than discovered later:

- **`hasTargetSRFs` is not implemented.** Deciding whether an arbitrary
  function is set-returning needs the catalog, which this predicate does not
  take. A body whose target list calls an SRF is currently ACCEPTED. Step 2
  must either take a catalog argument or refuse any target-list `FuncCall` it
  cannot classify — it must not inherit today's answer.
- **`rte->security_barrier` and the `rte->lateral` block are not ported**, and
  their absence is safe rather than merely unimplemented: both read a
  range-table entry PG built for a subquery-in-FROM, while a body reaching
  this predicate came from a qual sublink, where upstream's own
  `convert_EXISTS_sublink_to_join` (`subselect.c:1450`) synthesises the RTE.
  The flags are false by construction in the case this predicate serves.
- Re-basing a flattened body's correlation references (goopg represents them
  as `OuterColumnRef{Level:1}` in the PLANNED body) is step 2's obligation. It
  is deliberately NOT disguised as an admissibility question here.

## 4. Inertness, measured

Zero production callers: `sublinkBodyIsSimple` is called only from its tests,
and neither `Subquery` field is read outside the retention test. The gates are
run anyway, because "provably inert" is a claim about the change and gates are
how the claim is checked, not a reason to skip them.

| gate | result |
|---|---|
| `RALPH_PRECOMMIT_SCOPE=units` | PASS (exit 0, 0 FAIL) |
| `scripts/tpch-spotcheck.sh` | PASS — Q12=2, Q13=33 |
| `scripts/tpcds-sf025-regression.sh sweep` | PASS — `PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=3`; plan channel `same=99 changed=0 added=0 removed=0`; verdict-changes=none |
| `scripts/tpch-acceptance-arm.sh` | PASS — 24/24 labels MATCH on VALUES against a fresh same-loop baseline arm built from the unmodified tree |

**Movement: none**, and none was available: nothing reads what was added.

## 5. Step 2 — what landed (2026-09-20)

The splice took a different shape than the task's filed framing —
"splice the body's FROM items into the outer join list as REAL
relations ... discarding `.Plan`" — and the difference is load-bearing:
goopg plans sublink bodies EAGERLY at expression-resolution time, so
there is no unplanned body left to splice before planning; pulling
planning itself earlier is the M0145 IR's job. What step 2 does instead
is the plan-level equivalent — decompose the already-planned simple body
back into base-relation leaves at the SEAM, so the join search sees real
relations where it used to see one opaque synthetic leaf. Same
observational content for the search; no second planning pass, and `.Plan`
is discarded for the search's purposes only (execution still uses it).

### 5.1 The mechanism

- `Join.FlattenedRHS` (`plan.go`) marks a Semi/Anti join whose retained
  parser body passed `sublinkBodyIsSimple` AND whose planned RHS passed
  `decomposeFlatBodyTree` (`sublinkpullup.go`). Set at the three unnest
  sites (`unnestExistsExpr`, `unnestInExpr`, `unnestNonCorrelatedInExpr`).
- `decomposeFlatBodyTree` walks the planned RHS (SeqScan / Filter /
  Inner|Cross Join / positional-identity Project) into `leaves`, pooled
  `quals`, and an optional IN comparison `target`. `LeafLocal` filters
  are accepted only directly above a bare `*SeqScan` — there the
  leaf-local coordinate space IS the one-leaf subtree space, so the same
  `+base` shift lifts them. Output must equal the concatenated leaf
  schemas exactly, or the correlation/inner reference coordinates cannot
  be trusted and the body stays opaque.
- `extractSearchLeaves` (`joinsearchseam.go`, `admitSemiAnti` arm)
  decomposes a marked RHS into one synthetic leaf per body relation:
  multi-bit `rhs` relsets, `bodyQuals` pooled alongside the link pred,
  `SpecialJoinInfo` preserved, and each leaf costed by the REAL
  base-relInfo path (`estimateBaseRelInfo` + catalog stats) rather than
  the synthetic-leaf fallback.
- IN arms re-wrap the stripped body in a positional-identity
  `IsolatedScope` `Project` — the exact convention the body's original
  root project carried — so the M0063/M0071 NLI/pushdown protection is
  restored STRUCTURALLY (`pickInnerSide` needs a bare `*SeqScan` right
  side) without a marker check in the NLI gate. EXISTS bodies keep
  their pre-existing bare-body NLI eligibility (M0063-0004).
- P0-H11 closed in the same change: `joinlistProblem.cumOffsets []int`
  → `leafSpans []leafSpan` (`relfromjoinlist.go`), so synthetic
  out-of-band ranges survive into the problem instead of being
  re-spanned contiguously. `leafSpanWindow` admits a group as one
  boundary only when its spans are contiguous; `cumulativeFromSpans`
  is deleted, `spansFromCumulative` survives as a test helper.

### 5.2 The new decline: `semianti-not-tail`

Flattening newly ADMITTED a chain class the on-qual gates used to refuse
first, and that admission exposed a latent construction assumption: c2's
problem builder requires every synthetic leaf to occupy a TAIL slot
(`scans[nprefix:]`), but a demoted mid-chain ANTI — Q78's
`web_sales LEFT JOIN web_returns ... IS NULL JOIN date_dim` — walks as
`[real, synthetic, real]`. Construction then binds the trailing real
leaf as synthetic and vice versa, and a clause referencing the
synthetic RHS column reached `translateToLayout` on a real-only layout:
`join clause references binding column 75 (wr_order_number) ... not among
the 7 output columns` — reproduced in a unit fixture and in production
on Q78. Fix is the explicit tail check (`syntheticBits ==
leafRangeRelSet(nprefix, len(scans))`), declining `semianti-not-tail` so
the chain falls back to the marker-join plan it always produced.
Arbitrary synthetic-leaf placement (leaf reorder + relset remap) is
deferred to the ledger — it is real work, not a guard to relax.
Unnest-produced Semi/Anti links are always chain-TOP, so the splice's
own output is never declined by this gate.

### 5.3 Gates

| gate | result |
|---|---|
| `RALPH_PRECOMMIT_SCOPE=units` | PASS (exit 0) |
| `internal/optimizer` suite | PASS — incl. new `flattened_rhs_test.go` (decompose shapes/declines, scope-project round-trip, schemaIsLeafConcat, leafSpanWindow, EXISTS single+multi, IN, NOT IN, pruned-body decline, `TestSeamDeclinesRealLeafAfterSyntheticLeaf`) |
| `scripts/tpch-spotcheck.sh` | PASS — Q12=2, Q13=33 |
| `scripts/tpcds-sf025-regression.sh sweep` | PASS — `PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=3`; Q78 back to PASS 15 rows ck=c06cf981a7819a37 (oracle-verified) |
| `go vet` | clean |

## 6. Step-2 movement, measured

Expected per the task: the `leaf-count` decline class shrinks from 26
across 11 queries and the five census witnesses' join-method records
move. Measured on a fresh `GOOPG_PGSHAPED_DP_TRACE=1` SF0.25 EXPLAIN
capture (`analysis/m0142/m0142-0008a-3i-route-a-step2-census.txt`):

- `leaf-count` is **unchanged at 26**. The flattening IS active —
  decline records carry `nleaves>nrels` (a multi-leaf decomposed RHS
  inside `nrels=3 nleaves=6` lateral and `nrels=2 nleaves=3` leaf-count
  records) — but no `problem rels=` line contains a flattened semi RHS
  member: every still-declining chain has a SECOND undecomposable member
  (`*Project` over composites, NLI, `*Gather`, `*CTEScan` — the
  M0144-0003a census's own finding) or hits another gate first.
- `semianti-not-tail`×3 is a NEW class — Q78's mid-chain demoted-ANTI
  chain, declined by §5.2's check; it converts rather than removes.
- Plan channel vs the pre-change baseline: `changed=4` (Q33/Q56/Q60/
  Q83), every diff a column-QUALIFIER only (`i_manufact_id` →
  `item_1.i_manufact_id`) — the flattened body's scan now names its
  real table in the hash-cond text. Same shapes, same costs.

So the honest record is: mechanism landed and exercised end-to-end in
unit fixtures (single+multi-table EXISTS, IN, NOT IN — real leaves,
multi-bit rhs, pooled bodyQuals, out-of-band spans), corpus-visible
movement zero. The remaining leaf-count declines are not sublink bodies
at all — they are the same already-planned composites M0144-0003a's
census measured, which only the M0145 jointree-first pipeline (plan the
FROM jointree before lowering any member) can reach.
