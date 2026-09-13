# R122 SCOPE — Slice B: join paths propagate narrowed widths

R121 Slice A narrows base-rel scan paths and their single-child wrappers,
and is **parity-neutral** because the narrowing dies at the first join:
a join path never sets its own `Path.NCols`, so `pathNCols` falls back to
`relNCols(joinrel)` = the **full sum of both inputs' whole schemas**
(`joinsearchlevel.go:632-643`). Only first-level joins see anything
narrowed. Slice B carries it up the tree.

This is the round R121's own gate names: *"if Slice B does not land in
the next round-cluster, R121 is dead code and gets deleted."*

Baseline (unchanged all session): TPC-H **6/15/0/1/0**, TPC-DS
**2/69/0/25/3/0**. Datum-size / `minimize_datum` stays CLOSED (R114).

## 1. The cut — one choke point, not seven constructors

Every join path is created inline inside an `addPath(...)` /
`addPartialPath(...)` call and carries both `Jointype` and `Children`:
hash `pathgen.go:112` and `joinpathsparallel.go:221`, merge
`joinpathsmerge.go:424` and `joinpathsparallel.go:436`, nested loop
`pathgen.go:164` and `joinpathsnli.go:345`,`:519`.

Propagate in **`addPath`/`addPartialPath`** (`path.go:930`, `:977`)
rather than in each constructor. This mirrors Slice A's sweep decision,
which the R121 review singled out as the thing to carry forward: it
cannot miss a producer, a future join producer inherits it for free, and
the rule lives in one place instead of seven that must agree.

**The timing is what makes this correct, and it must be stated because
it looks wrong at first glance.** The constructor computes the join's
own cost from its *children's* widths (`pathNCols(outer)`,
`pathNCols(inner)` — `pathgen.go:104-105`) *before* calling `addPath`.
Stamping the join path's own triple inside `addPath` is therefore too
late to affect its own cost — which is exactly right, because that
triple is consumed only when this path becomes a CHILD of a higher join.
Cost-before-stamp is the correct order, not a bug.

### The rule

For a path whose `Kind` is **explicitly one of** `PathHashJoin`,
`PathMergeJoin`, `PathNestLoop` — a whitelist, never "has two children"
and never "has a `Jointype`", because `parser.JoinInner == 0` is the
ZERO VALUE (`internal/parser/ast.go:728`) and `PathSetOp`
(`windowsetoppaths.go:375-384`) has exactly two children, so both of
those tests read a set-op as an inner join — when **both** children
carry a narrowed triple (`NCols > 0`):

- `NCols = pathNCols(outer) + pathNCols(inner)`
- `AvgVarBytes = pathAvgVarBytes(outer) + pathAvgVarBytes(inner)`
- `OutputWidth = pathWidth(outer) + pathWidth(inner)`

**except** for `JoinSemi` / `JoinAnti`, which publish the LHS only —
take the outer's triple alone. This mirrors `joinsearchlevel.go:632-643`
exactly, which is guarded by `joinPublishesInner(sjinfo)`. Key on
`p.Jointype`, **not** `joinPublishesInner`, which is not in scope at
these sites. **`JoinRight` publishes BOTH sides** — outer-only is
`JoinSemi`/`JoinAnti` and nothing else.

Plain addition mirrors the rel rule at `joinsearchlevel.go:633,:639`
(`unionColVarBytes`'s wider-on-collision rule governs a different
object — the by-name map — so simple addition is the faithful analogue).

### Decline rules (each prevents a currency mix)

1. **Either child un-narrowed ⇒ the join declines.** A sum that is
   narrow on one side and full on the other is not a currency.
2. **All three fields or none** — the R120 defect, one level up. Hash
   cost consumes `pathWidth` *and* `pathNCols`/`pathAvgVarBytes` on the
   same path (`pathgen.go:95-96` vs `:104-105`, and
   `joinpathsparallel.go:210-213`). NOT
   `hashjoin_pgtuplesizing.go:57-58` — that is inside
   `tracePGHashTupleGeometry`, which its own header calls
   "diagnostic-only … no path-selection role" and which is gated on
   `pathTraceEnabled`. The claim is true; justifying a decline rule with
   a trace function would invite the next round to weaken it.
3. **Never overwrite a non-zero `NCols`** — leave an already-stamped
   path alone (idempotence under any re-add).
4. **Decline when either child is index-only.** `NCols > 0` is NOT a
   proxy for "this round narrowed it": `pathindexonly.go:136-148` writes
   `IndexOnly`, `NCols`, `AvgVarBytes` and `OutputWidth`
   **unconditionally**, independent of the flag. Without this rule a
   declining rel whose cheapest path is index-only would have its IOS
   triple laundered into a join sum while the NLI path for the same
   joinrel — inner from `CheapestParameterized`
   (`joinpathsnli.go:288`), `NCols == 0` — declined, putting two paths
   of ONE joinrel into different currencies. That is verbatim the leak
   R121's review caught at the wrapper level and fixed at
   `narrowcostinputs.go:213-230`. Only a DIRECT index-only child needs
   testing: `inheritNarrowedWidths` already refuses one transitively.
5. The helper must be **nil-safe on the path itself** — unlike
   `addPartialPath` (`path.go:978`), `addPath` has no `newPath == nil`
   guard.

## 2. The residual this round must COUNT, not assume away

R121's review named it and R121 did not measure it (its P1 was recorded
**PARTIAL** for exactly this): A(iii) buys per-*rel* uniformity, not
per-*comparison* uniformity. At joinrel A ⋈ B where A narrows and B
declines (nil `ColVarBytes` — un-ANALYZEd table, subquery, CTE, VALUES),
the two hash orientations are priced in different currencies, biasing
**toward hashing the narrowable side**.

Slice B makes this worse before it makes it better: decline rule 1
means a single declining base rel un-narrows every join above it. So
this round MUST report, per corpus:

- how many base rels decline, **broken out by which of the five arms
  fired**: (a) collector declined (`NeededColsKnown == false`),
  (b) empty keep-set, (c) nil `ColVarBytes`, (d) a kept column absent
  from a SHORT/partial map — `tableColVarBytes` truncates to
  `min(len(tbl.Columns), len(tbl.Stats.Columns))` (`entrywidth.go:84-91`),
  so a partially-analysed table yields a prefix-only map — (e) non-table
  leaf;
- how many joins decline because a child was un-narrowed **or
  index-only** (rules 1 and 4), counted **per join**, not per rel;
- how many join *pairs* put a narrowed input against a declined one.

Scale is known in advance and is not small: of the 100 TPC-DS query
files, **31 contain a `WITH` clause and 28 a `FROM (`-subquery**. Every
such leaf has `ri.table == nil` ⇒ nil `ColVarBytes` ⇒ the rel declines
⇒ under rule 1 **every join above it declines**. So on roughly a third
to a half of TPC-DS the narrowing dies partway up the tree. Subtrees
below the poisoning leaf still narrow, so this is not a no-op — but it
does mean a per-rel census would be unreadable, which is why the counts
above are per-join and per-pair.

If that last count is large, **P3's category reading is confounded** and
the REPORT must say so rather than crediting or blaming narrowing.

## 3. Predictions

| # | Claim | Verdict on miss |
|---|---|---|
| P0 | Flag OFF bit-identical to pre-round HEAD, both corpora | leak ⇒ STOP |
| P1 | ON: a join path's triple is the sum of its children's (outer-only for SEMI/ANTI); all-three-or-none; never set when either child is un-narrowed **or index-only** (rule 4); never set on a non-join Kind — **a `PathSetOp` negative pin**, since it has two children and the zero-value `Jointype` reads as `JoinInner`; `OutputWidth > 0 ⟺ NCols > 0` on every path this round writes; **no `RelOptInfo` field is written**; idempotent. Plus an **end-to-end mutation-tested** pin that fails if the propagation is removed (R121's review found rev 1's pins could not fail) | any violation ⇒ STOP |
| P2 | **Values unchanged**: TPC-H digest 24/24, TPC-DS SF0.25 sweep PASS=96 MISMATCH=0, artefact stamped with the arm | ANY values move ⇒ STOP |
| P3 | No parity regression: TPC-H match ≥ 6, TPC-DS match ≥ 2, no category increases on either corpus | a match lost ⇒ STOP; a category rises ⇒ triage against the §2 census before attributing |
| P4 | **The narrowing now reaches a multi-level join**: on a NAMED ≥3-relation witness whose expected numbers are written down BEFORE the run, a **non-top** join's `pathNCols` is strictly less than `relNCols(joinrel)`. Must be non-top: the top join's triple has no consumer (the upper planner reads a prebuilt path over the finished node, `upperordered.go:83-100`), so a passing P4 there would prove nothing. A tie is only a miss if the witness was expected to narrow — a statement needing every column of both leaves ties benignly. Temporary dump, removed before commit | unchanged on a witness expected to narrow ⇒ propagation did not reach; re-audit, do not tune |
| P5 | **The census of §2 is reported** with real numbers | absent ⇒ the round is not done (R121's P1 was PARTIAL for this reason; do not repeat it) |

**No prediction claims parity will improve.** Slices A+B together still
leave the aggregate coordinate untouched (Slice C), and R121 measured
that TPC-H has nothing near a spill boundary. If P3 holds and parity is
flat again, that is the reportable outcome and the honest question
becomes whether the A+B+C chain is worth finishing at all — which the
REPORT must ask explicitly rather than deferring a third time.

## 4. Hazards

- **goopg's Sort does not project** (carried from R121, still not
  discharged): a narrowed join under a Sort under-charges a sort that
  runs at full width. Slice B widens the population this touches, so
  `sort-strategy` stays on P3's watch list.
- **`OutputWidth` addition is not obviously right.** `pathWidth` falls
  back to `p.Rel.Width` for an un-narrowed path, so summing two children
  where one fell back would add a *relation* width to a *narrowed* one.
  Decline rule 1 prevents it; a pin must assert it.
- **Idempotence**: `addPath` may be reached more than once for a logical
  candidate across orientations; rule 3 covers it.
- The R121 flag `GOOPG_NARROW_COST_INPUTS` gates this too — Slice B is
  not a separate switch. One arm, so the A/B stays two-valued.

## 5. Gates (FOREGROUND)

1. Suites green; `go vet`.
2. Pins per P1, including the mutation-tested end-to-end one and a
   SEMI/ANTI outer-only pin.
3. `scripts/tpch-spotcheck.sh` PASS.
4. Values P2, sweep artefact stamped with the arm.
5. Parity both corpora OFF and ON. **Re-take the OFF baseline** — R121's
   ON sweep opened a further stats epoch. TPC-H via
   `estimate-audit -plan-only` (per-connection stats — never the raw
   psql script); TPC-DS via `capture-tpcds.sh`; `work_mem` pinned.
6. The §2 census (P5), per join and per pair, with the five decline arms
   broken out.
7. **Capture a `hashsize.Choose` input/output pair at both widths.**
   R121's REPORT §4 asked for this explicitly and this SCOPE originally
   dropped it: TPC-H was byte-identical because nothing appeared to sit
   near a batch boundary, and without the geometry a second flat TPC-H
   result is uninterpretable — and §3's "is A+B+C worth finishing"
   question cannot be answered.
8. **Report merge-input-sort movement SEPARATELY from hash-geometry
   movement.** `sortPathFor`/`sortPathForBounded` price a merge-join
   input sort from `pathNCols(sub)`/`pathAvgVarBytes(sub)`/`pathWidth(sub)`
   (`joinpathsmerge.go:470-493`), and with Slice B `sub` can be a JOIN
   path — so a merge join over a narrowed subtree gets a cheaper input
   sort while goopg's Sort runs at full width. `costSortRunWithWidth`
   also decides spill, making this a **join-method** lever, not a
   rounding difference. Without separate attribution, "join-method +N"
   will be blamed on the wrong mechanism. (The upper-planner ORDER BY
   sort is NOT affected — its input is a prebuilt path on an upper rel.)
9. `make plan-gate`: run or record as a reasoned omission.
10. REPORT.md → agent review → `commit -n` + push.
