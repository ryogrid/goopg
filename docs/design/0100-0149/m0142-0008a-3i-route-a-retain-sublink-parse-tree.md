# M0142-0008a-3i-route-a step 1 — retain the sublink parse tree, and be able to classify it

Status: STEP 1 LANDED 2026-09-20 (inert by construction — zero production
readers). Step 2 (the flattening splice) not started.
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

## 5. Step 2, unchanged

Teach `unnestSubqueriesInPlan` to test a retained body with
`sublinkBodyIsSimple` and, when it passes, splice the body's FROM items into
the outer join list as REAL relations with its quals merged into the outer
predicate — discarding `.Plan` for that sublink — so the search that follows
sees base relations. The obligations already on the task stand: re-derive the
leaf arithmetic rather than carrying today's numbers, close the P0-H11
`cumulativeFromSpans` span round-trip in the same change, keep Q78's
`outer-over-derived` firewall intact, and re-base the correlation references.
