# R123 SCOPE (rev 2) — MEASUREMENT ONLY: what actually causes TPC-DS's 42,679 mixed-currency join pairs

Rev 1 proposed a fix (a cost-only needed-column set, to stop the
collector declining on `WITH`/set-op/window/grouping-sets). **Review
BLOCKED it and the block is correct: rev 1's lever cannot produce the
symptom it targets.** Rev 2 withdraws the fix and runs the measurement
R122 asked for and rev 1 skipped.

No source change ships from this round beyond temporary, removed
instrumentation.

## 1. Why rev 1 was wrong — the arithmetic that refutes it

`neededCols`/`neededColsKnown` is a **single pair of fields on
`searchCtx`** (`joinsearch.go:195-196`), stamped identically onto every
rel of a search (`joinsearch.go:564`, `joinsearchlevel.go:608`) and
populated once per `planSelect` (`planner.go:1514`, `:1604`).

So a collector decline is **search-problem-uniform** — note the unit:
a CTE body and a derived table each get their own `planSelect` and their
own verdict, so one SQL statement can hold several. A `planSelect`
invocation whose gate trips has *no* narrowed rel at all — index-only paths are excluded too
(`pathindexonly.go:22` requires the set be KNOWN) — so **every one of
its joins lands in R122's "both un-narrowed" bucket and it contributes
ZERO mixed pairs.**

R122's own census then bounds the effect:

```
73,349 narrowed + 8,588 both-un-narrowed + 0 rule-4 + 42,679 mixed = 124,616
42,679 / 124,616 = 34.25%     ← the reported 34.2%
 8,588 / 124,616 =  6.89%     ← the ENTIRE collector-decline population
```

Collector declines are at most **6.9%**, not 34.2%. Every mixed pair
comes from a statement the collector **accepted**. And the direction is
wrong too: removing collector declines moves joins *out* of
"both un-narrowed" into narrowed **and mixed**, so rev 1's own P3
("34.2% → under 10%") is a bar the arithmetic already answers — the
share can only hold or **rise**.

Rev 1's headline — *"That is precisely the 34.2%-vs-0.07% split"* — was
false, and it rested on a **static text grep** over `.sql` files being
passed off as the runtime AST measurement R122 explicitly asked for.
The grep was also wrong on its own terms: 100 files not 99, 45
gate-tripping not 48, and **0** queries trip `WindowClause` (the gate
term is the named `WINDOW w AS (…)` clause; the 10 "window" queries
decline at a different site, `collectExprColumnNames`'s
`FuncCall.Over != nil` arm).

## 2. The leading hypothesis this round must test

From the review, and it fits the corpora far better than the collector
does: **non-whitelisted child kinds.**

`narrowJoinWidths` whitelists only `PathHashJoin`/`PathMergeJoin`/
`PathNestLoop` (`narrowcostinputs.go:227-231`), and
`inheritNarrowedWidths` covers only Gather/GatherMerge/Sort/Memoize
(`:287-315`). Every `PathSetOp`, Append, SubqueryScan, HashAgg or Unique
in a spine publishes `NCols == 0` while its sibling narrows — a mixed
pair at **every join above it**. TPC-DS has 22 set-op queries and heavy
Append/aggregate spines; TPC-H has neither. That shape matches
34.2%-vs-0.07% in a way the statement gate provably cannot.

It is a hypothesis, not a finding. This round measures.

## 3. What is measured (all temporary, removed before commit, raw dumps RETAINED)

1. **Mixed-pair ROOT-CAUSE histogram — root, not immediate kind.**
   Keying on the un-narrowed child's `Path.Kind` alone would be
   **uninterpretable**: an un-narrowed child is usually itself a
   `PathHashJoin` that failed rule 1 one level down, so a single root
   cause at the leaf of a 12-way TPC-DS spine manufactures ~12 mixed
   pairs, 11 of them reporting `PathHashJoin`. The histogram would say
   "hash joins cause mixed pairs" — true and useless — and every real
   bucket would fall below any threshold by construction.
   So: when a path fails to publish a triple, **stamp the reason** on it
   (`nonWhitelistedKind`, `relDeclineArmC`, `relDeclineArmB`,
   `indexOnlyRule4`, `wrapperGap`, `childCascade`); when a mixed pair is
   booked, walk the un-narrowed child's tag down to the first
   non-`childCascade` reason and bucket **that**. Report BOTH the raw
   per-`Kind` histogram (which makes the cascade visible) and the
   root-attributed one (which §5 acts on).
2. **Per-gate-term decline counter, top-level vs nested.**
   `collectStmtColumnNames` is **re-entrant** — called from the sublink
   arms of both collectors (`pathindexonlyneed.go:220,226,228` and
   `:387,401,403`) — so a naive counter at the function head books every
   nested EXISTS/IN/scalar subquery as a statement AND double-books,
   since a nested `false` also makes the outer call decline. The counter
   must distinguish the **top-level entry** (`neededColumnNames`
   at `:39`, the verdict that actually reaches `ctx.neededColsKnown`)
   from nested ones, and record for a top-level decline whether the
   cause was its own gate term, its own `default:` arm, or a nested
   decline (and if nested, the nested cause). Count
   `collectOutputColumnNames`'s twin gate (`:93-95`) separately or not
   at all — never into the same bucket.
3. **Rel-decline arms in R122's order**, keeping its zero-valued arms
   (b) empty keep-set, (d) short map, (e) non-table explicitly in the
   table: a nonzero is itself a finding, and their presence is what
   makes the two censuses structurally comparable.
4. **Per-query attribution.** Bucket by query id too. TPC-DS q64/q14
   generate orders of magnitude more join costings than q3, so a
   corpus-wide 60% could be one query. Every §5 figure is reported both
   by count and by number of distinct queries affected.
5. **Chosen-plan restriction — the number the parity question needs.**
   Most mixed comparisons never decide anything. Report a third figure:
   mixed comparisons where `addPath` actually **evicted** the other
   candidate. A 34% corpus-wide share with a 2% decisive share would
   change the recommendation entirely.
6. **Outer vs inner side** (one bit): the SEMI/ANTI arm publishes the
   LHS triple only, so an ANTI join above a narrowed inner is a
   deliberate design decision that would otherwise read as a decline.
7. **State the denominator's definition.** R122's 124,616 "join
   costings" is undefined (per `narrowJoinWidths` call? per `addPath`?).
   Define it in the REPORT next to the number.

## 4. Protocol — and why R122's 34.2% is NOT a valid baseline

R122's census scaffolding was removed and **its raw output was not
retained** (its own REPORT §10 says so), and its ON sweep opened another
stats epoch. A new number compared against 34.2% would therefore be a
reimplemented counter measured over a different join-costing population.

**There is no OFF baseline to take.** All four narrowing entry points
are flag-gated (`narrowcostinputs.go:91`, `:171`, `:215`, `:288`), so
with `GOOPG_NARROW_COST_INPUTS` unset nothing narrows: the OFF census is
`100% both un-narrowed` by construction, for every corpus, forever. An
OFF/ON pairing is meaningful for **plan captures**, not for this census.

So: **one ON census at a pinned epoch, reported as an absolute
distribution**, with R122's 34.2% cited only as a prior reading whose
epoch and counter implementation both differ. The OFF arm is used only
to assert the instrumentation is inert when the flag is off.

**Epoch controls (rev 1 pinned these and rev 2 initially dropped them
while making epoch stability load-bearing):** SF=0.25 gate cluster on
:65437 (NOT :65436 — the population differs by orders of magnitude),
`work_mem` pinned, autovacuum off, no re-ANALYZE and no server restart
between captures.

**TPC-H is the counter's sanity check, and it is a GATE, not an
implication.** R122 measured 0.07% mixed there; a reimplemented counter
that does not reproduce a near-zero TPC-H mixed share is broken before
its TPC-DS number means anything.

## 5. Pre-registered decision table (the round's deliverable)

**Distribution-first, threshold demoted to a tiebreak.** A single-bucket
share is sensitive to spine depth even after root attribution, and a
share of *costings* is not a share of *plan impact* (§3 item 5). What
stays pre-registered is the ACTION, not an arithmetic trigger:

| finding | verdict → next round |
|---|---|
| **FALSIFIER — statement-gate declines appear in the mixed-pair histogram AT ALL (> 0)** | the census contradicts §1's arithmetic (they must contribute exactly zero). **STOP and re-audit the instrumentation before reading any other row.** |
| largest root cause = non-whitelisted child kinds | **R124 = coverage round**: extend `narrowJoinWidths`/`inheritNarrowedWidths` to Append/SetOp/SubqueryScan/aggregate/Unique wrappers. Slice D stays dropped |
| largest root cause = arm (c) nil `ColVarBytes` | **R124 = stats round**: non-table leaves (CTE/subquery/VALUES) carry no per-column stats; that is the lever |
| largest root cause under 40% (no dominant cause) | **R124 is a PROMOTION-DECISION round, not a coverage round** — report the distribution and decide whether to ship what works on TPC-H |
| decisive (plan-evicting) mixed share is near zero, whatever the distribution | the confound never moved a plan; TPC-DS's flat categories were a real null after all, and R124 is a promotion-decision round |

Every row is reported with its share, its per-query spread AND its
decisive share (§3 items 4-5). No lever is implemented on a plurality
without the REPORT saying it is a plurality.

**A "the chain is not worth finishing for TPC-DS" verdict is a permitted
and possibly correct outcome.** R122 already established the ceiling:
TPC-DS's dominant categories are `join-order` (90) and `parallelism`
(86), which this chain does not touch; the realistic upside is a subset
of `join-method` (62) and `scan-type` (57). If the census shows the
remaining mixed pairs need a large coverage programme to clear, the
honest recommendation may be to promote what works on TPC-H and stop.

## 6. Bounds

- **No production change.** Instrumentation only, removed before commit;
  `grep` proof in the REPORT.
- `NeededCols`/`NeededColsKnown` untouched — no correctness path moves.
- No values gate needed (nothing ships), but the REPORT states that
  explicitly rather than omitting it.
- Findings carried forward for whichever lever wins, so a successor need
  not re-derive them:
  - **B3**: `CommonTableExpr` has TWO bodies — `Query *SelectStmt` and
    `DMLBody Stmt` (`parser/ast.go:794-801`); the doc's "Stage A rejects
    data-modifying CTE bodies" is **stale** (parser/analyzer/optimizer
    all handle them). Any future descent must `return false` on
    `DMLBody != nil`, or it silently skips that body's columns.
  - **Set-op shape**: for `s.SetOp` the **left operand is `s` itself**
    (fall through to its own Targets/From), while a `SetOpOperand`
    grouping node has no Targets/From of its own (`ast.go:927-931`).
  - **Grouping sets need no walk at all**: `prepareGroupingSets`
    (`planner.go:959`) rewrites `s.GroupBy` into the union of every
    set's expressions *before* the collector runs
    (`groupingsets.go:49-74`), so deleting `s.GroupingSets != nil` from
    the gate is the entire change, and adding a `Sets` walk would be
    cargo-cult.
  - **The real window blocker** is `collectExprColumnNames`'s
    `FuncCall.Over != nil` arm, not `WindowClause`.
  - **B4, and it must not be lost**: on any statement where
    `NeededColsKnown` is false but a cost-only set narrowed, the
    invariant `joinKeepSet ⊆ buildKeepSet ⊆ neededKeepSet`
    (`narrowcostinputs.go:41-46`) **inverts** — `narrowBuildInput`
    (`GOOPG_NARROW_BUILD`, default **ON**) inserts no Project, so the
    executor builds full width while the planner prices narrow. Planner
    under-pricing, i.e. R120's defect in reverse. Any revived Slice D
    must name this and add a timing check.
  - Ordinals (`GROUP BY 1`) are safe (`IntegerConst`, no column);
    `SELECT *`, NATURAL, LATERAL, TABLESAMPLE and table functions
    already decline.

## 7. Gates

1. Suites green; `go vet` (instrumentation must not break either).
2. Censuses captured at one pinned epoch per §4 (TPC-DS SF0.25 on
   :65437 and TPC-H as the counter sanity check), raw dumps retained and
   **committed** as artefacts — R122's were not, and its figures are
   consequently unreproducible.
3. **The instrumented build must be plan-identical to a clean ON
   capture.** A counter that perturbs path selection censuses a
   different planner. Assert-only instrumentation: counters, no early
   returns, no behaviour in a cost function.
4. Instrumentation removal proved **literally, not textually**: the
   round commits **zero** Go changes (`git diff` over `internal/` empty
   at commit time), not merely a clean `grep`.
5. `make plan-gate`: not applicable — nothing ships and the flag is
   default-off — but say so rather than omitting it.
6. REPORT.md with the §5 decision table resolved → agent review →
   `commit -n` + push.
