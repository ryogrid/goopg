# R121 SCOPE (rev 2) — narrow the planner's COST inputs: Slice A only (scan constructors + wrapper propagation)

Rev 2 remediates a BLOCK review (B1–B5 + notes 1–10). The biggest
changes: **P5 is withdrawn as structurally unsatisfiable**, **Slice B is
deferred**, and **Slice A is re-specified at the path constructors plus
a wrapper-propagation rule** — as written, rev 1's Slice A would have
reached almost nothing on a parallel corpus.

Motivation (R120): goopg's per-group entry for TPC-H Q10 is **3888 B vs
PG's ~224 B (17x)**, of which **1776 B is `48*ncols` over 37
concatenated columns** where PG carries ~7. R120's PG-cited currency
correction came out net-negative purely because it was applied on
un-narrowed `ncols`.

Baseline: TPC-H **6/15/0/1/0**, TPC-DS **2/69/0/25/3/0**. Datum-size /
`minimize_datum` stays CLOSED (R114) — this changes no constant, only
how many columns it multiplies.

## 1. The gap (citations corrected — note 1)

- `Path.NCols`/`AvgVarBytes`/`OutputWidth` (`path.go:268-276`) are read
  preferentially by `pathNCols`/`pathAvgVarBytes`/`pathWidth`
  (`path.go:648-686`), falling back to the rel.
- Written at **exactly one** production site:
  `pathindexonly.go:143-148` (index-only scans). Everything else falls
  back to `relNCols` = full leaf schema (`joinsearch.go:397`); join rels
  **sum both inputs' full counts** (`joinsearchlevel.go:632-643`).
- Narrowing today is **entirely post-selection** (`narrowoutput.go:53`,
  `:111`; `deriveJoinKeeps` at `createplanroot.go:122`) and **no cost
  function reads it**.
- Order in `searchOneProblem`: `buildInitialRels` **`:668`** (builds AND
  costs the one prebuilt SeqScan per base rel, `joinsearch.go:435-441`);
  `stampNeededColsOnRels()` **`:699`**; `stampOutputColsOnRels()`
  **`:700`**; then `setBaseRelConsiderParallel` **`:707`**,
  `addBaseRelPartialPaths` **`:708`**, `addBaseRelIndexPaths` **`:709`**,
  `addBaseRelGatherPaths` **`:724`**.

## 2. Why narrowing is safe in the direction that matters

`NeededCols` is a set of column **names** and `neededColumnNames`
(`pathindexonlyneed.go:14-27`) deliberately **over-states** (a name
needed for `orders` counts for `customer` too) and **abandons the whole
set** on any shape it cannot enumerate. `NeededColsKnown == false` means
*"collector declined — do not narrow"*, explicitly not an empty set
(`path.go:441-445`). So narrowing can only ever keep **too many**
columns. Ordering of the three keep-sets:
`joinKeepSet ⊆ buildKeepSet ⊆ neededKeepSet`, i.e. **planner-narrowed ⊇
executor-narrowed** — the safe direction (note 8).

## 3. Narrow on the Path, never the rel (confirmed)

1. `buildAvgVarBytes` (`entrywidth.go:49-71`) reads
   `build.Rel.AvgVarBytes`/`ColVarBytes` and **never** the Path, using
   the whole-relation sum as its deliberate **over-charge decline**
   value for the executor's HashJoin entry (`createplanjoin.go:572`).
   Narrowing the rel turns that into an under-count — which
   `entrywidth.go:32-36` says makes the executor "believe a build fits
   in memory when it does not".
2. Rels are per-relset **singletons** offered to both orientations
   (`joinsearchlevel.go:668-673`), and `:632-643` propagates rel figures
   through the whole DP.

## 4. Slice A, re-specified (B2, B3, B5)

Rev 1 said producers after the stamps "pick the narrowed figures up
naturally". **That is false** — they construct fresh `Path` literals
that set no width triple: `addPartialSeqScanPath`
(`considerparallel.go:704-716`), `makeGatherPath`/`makeGatherMergePath`
(`gatherpaths.go:216-263`), `sortPathForBounded`
(`joinpathsmerge.go:495-508`). Since goopg's TPC-H bench plans are
**all parallel**, nearly every join child is a partial scan or a
Gather, so a one-shot back-fill at the stamps would narrow ~nothing.

### A(i) — write the triple at every base-rel scan constructor

Compute once per base rel, from `scanPathTarget(rel)`
(`narrowoutput.go:759-777`), and apply at **every** base-rel path
constructor:

| producer | site |
|---|---|
| prebuilt SeqScan | `joinsearch.go:440` — back-filled **after `:700`, before `:707`**, since it already ran |
| partial SeqScan | `considerparallel.go:704` |
| bitmap heap / and / or | `pathbitmap.go:75`, `:82`, `:530` |
| **parameterised index** | **`pathparamindex.go:416`** — the NLI inner-side producer, heavy on TPC-DS; it already calls `scanPathTarget(rel)` for `Target` at `:413-415`, so the seam is open |
| **index-ordered (serial)** | **`pathindexordered.go:251`** — distinct from plain index; its partial twin at `:290-297` needs no separate write (`twin := *serial` copies the triple) |
| index-only (+ partial twin) | `pathindexonly.go:157`, `:180` — **exempt, see below** |

The back-fill needs **no re-costing and no re-`setCheapest`**:
`costSeqscan(cp, relPages, relTuples, numQualOps)`
(`cost_funcs.go:192-196`) is width-independent, and
`baseSeqScanCostInputs` (`joinsearch.go:479-488`) uses width only via
`estScanPages` on the non-`SeqScan` fallback, already computed earlier.

**Index-only paths are the one documented exemption**, and the reason
matters: their `covered` set is a genuinely narrower **emitted** schema,
so their cost advantage is real rather than a currency artefact. They
also derive `AvgVarBytes` from `coveredAvgVarBytes`
(`pathindexonly.go:242-259`), which reads `tbl.Stats` and **returns 0
when `Stats == nil`** — it never consults `ColVarBytes`. So an
un-ANALYZEd indexed table can carry a triple on its index-only path
while A(i) declines every other path of that rel; that is pre-existing
at HEAD and is why the A(iii) invariant is scoped to the paths this
round writes.

- `NCols = len(keep)`; **`len(keep) == 0` ⇒ decline entirely** (note 3 —
  `SELECT count(*)` has an empty keep-set, and `NCols > 0` is
  unimplementable there; `buildAvgVarBytes` already declines to `full`
  at `entrywidth.go:55-57`).
- `AvgVarBytes` = Σ `rel.ColVarBytes[strings.ToLower(name)]` over the
  kept columns, **declining to `rel.AvgVarBytes` on any unattributed
  column** (`buildAvgVarBytes` semantics — fails HIGH). Note the
  coordinate conversion: `neededKeepSet` matches names
  **case-sensitively** while `ColVarBytes` is **lowercase-keyed**
  (`entrywidth.go:90`) — the `ToLower` is a required step, not an aside
  (note 5).
- `OutputWidth` via `TupleWidth` over the kept `SchemaColumn`s
  (`indexOnlyOutputWidth`, `pathindexonly.go:167-175`, is the model).
- **All three or none** (B4): a path must never carry a narrowed
  `NCols`/`AvgVarBytes` with a full-width `pathWidth`, because hash-join
  cost consumes both currencies on the same path (`pathgen.go:95-96`
  vs `:104-105`; `hashjoin_pgtuplesizing.go:57-58,:87`;
  `joinpathsparallel.go:210-213`). That would recreate R120's own
  two-currency defect one level up.
- `NCols > 0` must accompany any narrowed `AvgVarBytes` —
  `pathAvgVarBytes` gates on it (`path.go:664-666`).

### A(ii) — wrapper paths propagate their child's triple

Single-child wrappers must **copy** the child's triple verbatim (they
project nothing): `makeGatherPath`, `makeGatherMergePath`
(`gatherpaths.go`), `sortPathForBounded` (`joinpathsmerge.go:495`),
and **`PathMemoize`** — which is a single-child wrapper
(`joinpathsmemoize.go:253-270`, `Rel: innerPath.Rel`,
`Children: []*Path{innerPath}`), **not** a join path as rev 1 wrongly
listed it (B3). There is deliberately **no `PathMaterial` kind**
(`joinpathsmemoize.go:449`, `joinpathsmergeouter.go:67` both say so) —
rev 2 listed one in error; it is removed.

Also switch the one remaining **direct rel read** on this route:
`costMemoizeRescan` is fed `relNCols(innerPath.Rel)` directly at
`joinpathsmemoize.go:252`, bypassing `pathNCols` entirely — it would
never see narrowing otherwise. (The only other production direct read is
`joinsearchlevel.go:632-643`, which is Slice B's territory.)

### A(iii) — per-rel all-or-nothing (B5, load-bearing)

A per-path decline is **not a safe granularity**. If joinrel *R* holds
P₁ (children narrowed, costed at ~7 cols) and P₂ (a Gather/Sort/index
child un-narrowed, costed at 37), `addPath` compares two costs in
different currencies and keeps the "cheaper" — a **systematic bias
toward whichever shape happened to be narrowable**, invisible to any
values or category gate.

Therefore: narrowing is decided **once per base rel** and applied to
**every path of that rel that this round writes, or none**. If any such
constructor for a rel cannot narrow (nil `ColVarBytes`, unattributed
column, empty keep-set, `NeededColsKnown == false`), that rel narrows
**nothing**.

The pin asserts exactly that scoped form — *among the paths this round
writes, all or none, with index-only as the one documented exemption* —
NOT the unscoped "all paths of a rel". The unscoped form is already
false at HEAD (see the un-ANALYZEd indexed-table case above), so a pin
written that way could never pass.

**Residual, named rather than assumed away (B5 one level up):** A(iii)
buys per-*rel* uniformity, not per-*comparison* uniformity. At joinrel
R = A ⋈ B, `addPath` compares the two hash orientations — P1 prices
`innerCols: pathNCols(B)`, P2 prices `pathNCols(A)` (`pathgen.go:104`).
If A narrows and B declines, P2 is priced on the needed set and P1 on
the full width, biasing **systematically toward hashing the narrowable
side**. Decline is an over-charge, so the direction is knowable but the
comparison is still mixed. This is inherent to any partial narrowing;
it does not block the round, but it must be COUNTED (see P1) and, if
the count is large, P3's category movement is confounded and the REPORT
must say so instead of crediting narrowing.

### Deferred out of this round

- **Slice B (join-path propagation)** — needs narrowed children to exist
  first, and carries its own `OutputWidth`/SEMI-ANTI/`addPath`-currency
  questions. Note for it: key on `jt parser.JoinType` (`pathgen.go:78`,
  `:148`), not `joinPublishesInner`, which is not in scope at those
  constructors; and `JoinRight` publishes **both** sides — outer-only is
  `JoinSemi`/`JoinAnti` only (note 4).
- **Slice C (upper/aggregate input width)** — see §5.

## 5. Why P5 is withdrawn (B1)

Rev 1's headline experiment was: with R120's flag also ON, does Q10 stop
flipping? **It cannot, by invariant.** `aggInputWidth(child)` reads
`len(child.Output())` off the built node (`groupingpaths.go:327-333`),
and that node comes from `createPlanAtSearchRootRange`, which **must**
publish the full binding concatenation — it panics on any hole and only
permits a `fill`-licensed padded NULL (`createplanroot.go:100-140`). So
Q10's `inNcols=37`/`avgVar=2080` are unreachable from `Path.NCols`, and
rev 1 pre-registered a **guaranteed** outcome whose miss-verdict was
"delete R120's arm" — self-contradictory, and it would have deleted a
correct, PG-cited cost arm on an instrumentation artefact.

**R120's promote-or-delete expiry for `GOOPG_HASHAGG_WIDTH_CURRENCY` is
therefore re-pointed from R121 to Slice C**, and R120's REPORT §7 must
be amended to say so. Slice C is where the aggregate's input coordinate
is addressed (either the grouped rel takes its width from the chosen
path's narrowed figures, or the `joinKeepSet` derivation runs before
`aggInputWidth`).

## 6. Predictions

| # | Claim | Verdict on miss |
|---|---|---|
| P0 | Flag OFF bit-identical to pre-round HEAD, both corpora | leak ⇒ STOP |
| P1 | ON: narrowed `NCols`, `AvgVarBytes` and `OutputWidth` are each **non-increasing** vs un-narrowed on every path that carries them; the triple is all-three-or-none; **per rel, all paths or none** (A(iii)); nothing is set where `NeededColsKnown == false`; no wrapper carries a triple inconsistent with its child. Asserted by a walk, not by eye. **Also census, per corpus: how many base rels declined, and how many join pairs put a narrowed input against a declined one** (the A(iii) residual) | any widening or a mixed rel ⇒ STOP; a large declined/mixed census ⇒ P3 is confounded, report it rather than crediting narrowing |
| P2 | **Values unchanged**: TPC-H digest 24/24, TPC-DS SF0.25 sweep PASS=96 MISMATCH=0, sweep artefact stamped with all three narrowing/currency flags | ANY values move ⇒ STOP (costing-only) |
| P3 | TPC-H match **≥ 6**, TPC-DS match **≥ 2**; `join-order`, `join-method` **and `sort-strategy`** non-increasing on both corpora (sort included per note 6) | a category rises ⇒ triage before proceeding; a match lost ⇒ STOP |
| P4 | On a **hash-join** witness, the build side's `pathNCols` drops from the full concatenation toward the needed set, measured by the R120-style per-node dump (temporary, removed before commit). **Bounded by design:** with Slice B deferred, the effect reaches only joins whose children are base-rel paths or A(ii) wrappers over one; at level >= 3 the child is a join path with no triple and `pathNCols` falls back to the full joinrel (`joinsearchlevel.go:632-643`). Q10's *aggregate* input is explicitly NOT expected to move (§5) | unchanged **at a first-level join** ⇒ A(i)/A(ii) did not reach the constructor; re-audit, do not tune. A small overall delta is EXPECTED and is not a miss |

## 7. Hazards (note 2 — rev 1 named the wrong pin)

- Rev 1 claimed `pathtarget_test.go:855,866-878` would trip. It will
  **not**: that is `TestDeriveJoinKeepsWitnessShape`, which snapshots a
  hand-built tree (`slice3WitnessTree`, `:764-804`) that never runs the
  search. Nor will `TestGenerateScanPathsCarriesTarget` (`:226-236`),
  because `generateScanPaths` has **no production caller** —
  production builds `newPrebuiltPath` (`joinsearch.go:435`,
  `path.go:706-708`), a `PathPrebuilt` carrying neither `Target` nor the
  triple. **So no existing pin covers the production producer: this
  round must add one.** The rel-identity assertion
  (`p.Rel.NeededCols` is the statement set, not a copy) must keep
  passing untouched.
- **goopg's Sort does not project** (note 6): a narrowed scan under a
  Sort under-charges a sort that really runs at full width. This is why
  `sort-strategy` joins P3's non-increasing list.
- **Three-flag interaction** (note 7): with `GOOPG_NARROW_COST_INPUTS=1`
  and `GOOPG_NARROW_BUILD=0`, `hashsize.Choose` solves for a geometry
  the executor will not build — exactly the under-count
  `entrywidth.go:34-36` warns of. Either make the new flag **imply**
  `narrowBuild`, or pin the combination and stamp all three flags in
  every artefact.
- `rel.ColVarBytes` is nil for un-ANALYZEd tables, subqueries, CTEs and
  VALUES (`joinsearch.go:411` is guarded) ⇒ those rels decline.
- `path.go:286-288`'s "NEVER applied: no createPlan change, no cost
  change" is already half-stale (Slice 2 wired `buildKeepSet` in); the
  **cost** half is what this round changes. Fix the comment, and update
  `Path.NCols`'s "what THIS PATH emits" doc (note 8) with the
  `⊇ executor-narrowed` argument.

## 8. Flag

`GOOPG_NARROW_COST_INPUTS`, strict `== "1"`, **default-off**, registered
in `flaglabels.go` + `scripts/planner-flags.env`. Note the existing
`GOOPG_NARROW_*` flags are opt-**out** (default ON) and gate plan shape
only; this is the first to gate a **cost** input, and must not be
confused with them in any artefact.

## 9. Gates (all FOREGROUND)

1. Suites green; `go vet`.
2. Pins: the A(iii) all-or-none-per-rel invariant, decline on
   `NeededColsKnown==false` / nil `ColVarBytes` / empty keep-set /
   unattributed column, the all-three-or-none triple, wrapper
   propagation (incl. Memoize + `costMemoizeRescan`), case conversion,
   and OFF-is-inert. Plus the missing production-producer pin (§7).
3. `scripts/tpch-spotcheck.sh` PASS.
4. Values P2, sweep stamp naming the arm.
5. Parity both corpora OFF and ON. **Re-take the OFF baseline** — R120's
   ON sweep opened a third stats epoch; do not reuse R120's captures.
   TPC-H via `estimate-audit -plan-only` (per-connection stats — never
   the raw psql script); TPC-DS via `capture-tpcds.sh`; `work_mem` and
   all three flags pinned per arm.
6. `make plan-gate`: recorded as a **reasoned omission** if not run —
   default-off flag plus P0 bit-identity means its structural pins
   cannot move. State it; do not leave it absent (R120 took a review
   note for exactly that).
7. REPORT.md → agent review → `commit -n` + push.
