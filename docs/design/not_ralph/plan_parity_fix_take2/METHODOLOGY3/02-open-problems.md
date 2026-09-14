# 02 — Open problems

*Everything the record shows as unresolved at R130. Items verified fixed and
landed are excluded by construction; where a fix landed but left a named residue,
only the residue appears.*

*Identifiers: `B*` blockers, `C*` correctness risks, `N*` nuisances and smaller
items. Cross-references are to the round (`R118`), the K-ledger (`K65`, in
`TODO.md`), or a source path.*

---

# Part A — Summary

## The twelve blockers

| id | problem | corpus impact | status |
|---|---|---|---|
| **B1** | **Executor-side narrowing (projection pushdown / `DatumBytes`) does not exist — and is project-level declined.** | caps TPC-H near 6–7/22 | decision, not task |
| **B2** | `join-order` is the largest category on both corpora and its **costing** half is unsolved. | TPC-H 14, TPC-DS 89 | candidate half landed (R51), moved 0 |
| **B3** | TPC-DS `parallelism` 87/99, with a non-planner floor underneath it. | TPC-DS 87 | partly attacked (R94/R95) |
| **B4** | `aggregation-strategy` has **no working lever**; both attempts are dead. | TPC-H 10, TPC-DS 69 | K12 slice 3 named, unscoped |
| **B5** | PG's hash **final-cost inputs are unobservable**, and Q96's residual is stuck on them. | TPC-DS Q96 + family | R98 UNOBSERVABLE, R99 ORACLE INVALID |
| **B6** | goopg's executor has **no Memoize on the NL probe path R59 repriced** — a *toward-oracle* change took DS Q72 from 4s to 320s TIMEOUT. | 1 query, class-wide risk | unfixed since R59 |
| **B7** | **Should goopg reproduce PG's estimation errors?** Unanswered, and load-bearing. | Q9, Q4, possibly join-order | escalated, unresolved |
| **B8** | `indexProbeCostMultiplier = 2.0` puts **plan parity and wall-clock in direct conflict**. | corpus-wide | named R58, never scoped |
| **B9** | The **executor is 4.1× slower than PG** on TPC-H SF1 and 10–20× on typical TPC-DS queries. | all | no owner round |
| **B10** | **`corr = 0` fallback** prices an index scan with no correlation slot at `max_IO_cost`. | corpus-wide | root-causes §7 priority 4; persistence half CLOSED (K27), fallback half open |
| **B11** | The **forced-shape route bypasses the path-cost seam entirely**, invalidating a whole class of experiment. | methodology | F10; no guard exists |
| **B12** | `make plan-gate` fails 20/22 against a baseline **un-refreshed for ~125 rounds**. | all gating | standing opt-out since R65 |
| **B13** | **TPC-H `parallelism = 0` is a protocol artefact** — the baseline is captured `-serial`, so the category is measured *out*, not closed. | TPC-H, size unknown | last real reading: 18→16 under the flip (R43 rev 3) |

## The three correctness risks

| id | problem | why it is invisible |
|---|---|---|
| **C1** | `ParamRef` LIMIT + DISTINCT still returns **wrong rows**. | fail-closed, zero corpus impact today |
| **C2** | **Values-green sweeps cannot detect qual-placement loss.** | proved by R56's Q78 — checksums passed while three filters vanished |
| **C3** | A **second display/estimator seam** was seen on Q22 and never root-caused. | "gates did not complain" |

## Nuisances by theme

*(N12–N15 are unused; numbering is not contiguous.)*

| theme | ids | contents |
|---|---|---|
| **Per-database catalog defects** | **N1–N11** | FK DROP silently no-ops (N1); `HasPrimaryKey` same shape (N2); no in-process test crosses a DATABASE boundary (N3); `pg_constraint` returns 0 rows of any contype post-restart (N4); `PhysicalTypeIsVarlena` has no `IsArray` arm (N5); FK parent stored by name not OID (N6); `conpfeqop` family NULL (N7); no runtime `pg_constraint` index maintenance (N8); P6 never exercised (N9); `boundProven` misnamed (N10); six `deleteCatalogRowsForOID` sites hardcode `DefaultDBOid` (N11) |
| **Flag and code debt** | **N16–N19, N28–N30** | three default-off cost arms, one resolved-to-delete and undeleted (N16, N17); stale prose inverted by R128 (N18); shipped planner-below-executor `avgVar` debt (N19); stale bucket-charge patch + `MapSlotBytes` 2× low (N28); `pg-regress-runner.sh` not run for R128 (N29); `internal/parser` fails 60 tests (N30) |
| **Measurement instruments** | **N20–N27** | the K18 `$$` tempfile trap (N20); capture arm-stamping (N21); TPC-DS match=1 vs match=2 (N22); stats-epoch drift (N23); `pg_statistic` not queryable (N24); unreproducible prose figures (N25); reference oscillation + the K9 fixture (N26); census reproducibility (N27) |
| **Estimator and cost residuals** | **N31–N45, N54** | 4.000× parallel-rows inflation, designed-but-unlanded (N31); 91× Q7-class selectivity gap (N32); CTE inlining (N33); no partial-Append producer (N34); notional partial-agg rows (N35); ANALYZE blind in-transaction (N36); `FoldConstants` defects (N37); no volatility map (N38); Q96 residual (N39); parallel row estimates diverging both ways (N40); dimension-table pages (N41); truncation/threshold items (N42); bitmap-vs-index divergence (N43); un-consumed scan cost (N44); ten-plus-round ledger carries (N45); absorbed-leaf `index_qual_cost` (N54) |
| **Seam, coverage and hygiene** | **N46–N53** | partial-NL Gather completion (N46); `add_partial_path_precheck` (N47); `reg*[]` + `round()` (N48); comparator never returns `costsEqual` (N49); synth-clause and assertion hygiene (N50); K67's residue (N51); TPC-DS carries no FKs (N52); `Materialize` correctly queued (N53) |

---

# Part B — Detail

## B1 — Executor-side narrowing does not exist, and is project-level declined

**This is the programme's binding constraint.** `FRONTIER.md` establishes that
TPC-H's two closest queries share it:

- **Q4** (R81): the election is decided one rel above the grouping contest, in
  `electOrderedGrouping` (`upperorderedgrouping.go:148`), on a **startup ratio
  against `stdFuzzFactor = 1.01`** — goopg 1.0086 (inside fuzz → tie-break →
  hashed), PG 1.0118 (outside → sorted). R81's unblock condition (i) is
  DatumBytes / projection pushdown — **"neither exists"**. The semi output is
  448 B against PG's 16 B, and the *widths ratio 28× exceeds the rows ratio 16.6×*.
- **Q9** (R69 §6, after the NLI audit): *"Slice (b) width/footprint — now the
  load-bearing half of Q9."* R77 closes with *"Next: rows/width program."*
  R70 measured the build at 743 MB / NBatch 8 and found even a 6-column
  projection floor **fragile**: +10% rows degrades to NBatch 2 with a +4k margin,
  inside plausible estimate noise. Fail-closed, no cut.

Every cheaper lever is measured and rejected (README §3).

**What partly exists.** `internal/optimizer/narrowoutput.go` narrows the hash
*build* side and upper nodes, default ON (`GOOPG_NARROW_BUILD`,
`GOOPG_NARROW_UPPER`, `GOOPG_NARROW_UPPER_SORT`). What is missing is narrowing at
the **scan leaves inside a join tree**, which is where the 20–32× width originates
— and structurally, *inside a join tree there is no `*Project` above the scan at
all*, so there is nowhere to hang a projection.

**What is declined.** `docs/design/not_ralph/minimize_datum/README.md` opens
**"Status: NOT APPROVED TO START"**, and its `REVIEW.md` records that the bundle
does not hold a licence from take3: §0.1's first clause, *"a new row
representation"*, has **no stated re-proposal path at all**. `05-work-estimate.md`
also distinguishes two changes people conflate: re-laying out `Datum` (~3,090
non-test sites, declined) versus changing the *retention* format (~70 non-test
sites, proposed). Confusing them is *"the single most likely way to misprice this
work."*

**So the two halves have different owners and different costs**, and it is
important not to treat them as one item:

| half | reach | approval status |
|---|---|---|
| projection pushdown (narrow what scans emit inside a join tree) | planner + create-plan; no `Datum` change | **not blocked by the minimize_datum decline** |
| **packed retention format** (`PackedTuple`/`PackedSlot`) | ~70 non-test sites (design 04) | explicitly declined; needs an owner decision |
| a `Datum` re-layout below 48 B | ~3,090 non-test sites | declined by take3 13 §10; **not proposed by anyone** |

**Name the second half correctly.** `minimize_datum/README.md` states: *"`Datum`
does not shrink. **It stays exactly 48 bytes** and stays the working format."*
What was declined is *a new row representation*. `05-work-estimate.md` §1 warns
that confusing the re-layout with the retention format is *"the single most
likely way to misprice this work"* — so "the `DatumBytes` half" is the wrong
label for it.

**A measured caveat on the Q9 half of this blocker**, which nothing else in the
programme's summaries carries. `internal/optimizer/entrywidth.go:38-48` holds a
permanent comment headed **"WHAT THIS DOES NOT BUY"**: correcting the entry width
does **not** change Q9's batch count, because `nbatch` is 4 at entry 112..194
alike, drops to 2 only in the narrow 96..111 window, and **returns to 4 below 96**
— the bucket array then doubles to 100.7 MB and takes back more than the rows gave
up. *"The lever on this witness is MapSlotBytes, not the entry."* And commit
`2e15b8ca3`'s body adds that a packed retention format would make Q9's batching
**worse**. Set against R129's finding that `MapSlotBytes` 48→96 is **parity-inert**,
the honest position is: **Q9's case for this campaign is measurably non-monotone
and weaker than Q4's.** Anyone funding option (a) on Q9's account must read this
first.

`05-work-estimate.md` §1.5 also prices three cheaper interventions against the
bundle, including deleting a duplicate build map (`lazyHash` **and**
`lazyIntHash` both maintained → ~2× peak build memory, *"one commit"*). None has
been taken up by this workstream.

**And the bundle's own adversarial review found two further blockers** that any
option-(a) decision must weigh, not just the missing licence: *"The premise was
modelled, and the measured answer is different — **and smaller**"*, and
*"Sequencing violates take3 13 §8.2."* The bundle asks rather than asserts, and
its value estimate is its own review's second finding.

## B2 — join-order: candidate half landed, costing half unsolved

TPC-H **14**, TPC-DS **89** — the largest category on both corpora.

R51 landed the transitive-equality candidate half and moved join-order **18 → 18
/ 95 → 95** (F6). Three attribution rounds then converged on pricing (R53, R68,
R96), and every pricing round terminated blocked: R70 BLOCKED on fragility, R89
C2 (missing inputs), R98 UNOBSERVABLE, R99 ORACLE INVALID.

Current sub-state:

- The **candidate half is open at HEAD** — `joinsearchseam.go:461` calls
  `inferTransitiveEqualities` unconditionally, and R51 adjudicated both Slice-3
  tests against PG. K26 §9.3's "constants-only" description is a pre-R51 snapshot
  and must not be read as current. What is open is the **costing** half.
- **Q9's route is entangled with B7**: goopg's estimate is 1.8× low and PG's is
  344× high, so it is not established that closing Q9's join-order gap is even
  the right objective.
- **Q96's route is blocked on B5.**

## B3 — TPC-DS parallelism, 87/99

K37: 66 of 99 TPC-DS queries PG plans parallel and goopg plans serial — **zero the
other way**; `Parallel Hash` goopg 0 vs PG 314. It feeds four categories at once.

Three separable sub-items:

1. **The `GOOPG_GATHER_PATHS` flip** (K38, K80, R10, R24). At `all`: Gather
   42→104, Parallel Hash 0→167 — *and parity does not improve*. R43 rev 3
   re-measured TPC-H `parallelism` 18→16 with no new match. R10 attempted it and
   **reverted**; its prerequisite is the R12/R13 failing-test set. **That set must
   be re-measured, not inherited**: the "5 failing tests, four needing
   adjudication" list (`TODO.md:2318-2326` —
   `TestSplitEqualityForHashMultiKey/searched_enumerator`,
   `TestSlice3LiveQ9ShapeDerivation`, `TestSlice3FilterColumnSurvivesNarrowing`,
   `TestOwnedBuildPoisonPrebuiltBoundary`) is **R43-era**, ~87 rounds stale, and
   at least one member (`TestSlice3LiveQ9ShapeDerivation`) was re-baselined by R51
   nine rounds later. The adjacent method note in the same ledger says it
   outright: *"re-measure before relying on any prior round's figures."*
2. **Q14's last category** (K92) needs PG's real execution model — workers building
   a shared hash from a partial inner. Relabelling would be misdescription, i.e.
   the plan-forcing the goal forbids. *"Q14's third match is NOT cheap."*
3. **A non-planner floor.** K14/K15: `relpages` is a planner **input**, and heap
   density drives worker-count differences no planner change can fix. K39 was
   closed by R31b for fact tables (PG-faithful to 0.4%, 4-worker plans 19→0), but
   **K41 remains open and unexplained** — dimension tables diverge the *other* way
   (`customer` 1,979 vs 2,872; `item` 716 vs 1,284).

**Two open on-disk items sit under this floor and are easy to lose**, both still
unchecked in the round list and both named by `ROADMAP-to-all-match.md` §5 as
un-closeable by planner work:

- **R22 (heap page fill on bulk load)**, `TODO.md:915-918` — goopg leaves ~21.9
  B/row of free space PG does not, ~15% on `store_sales`.
- **R23 (`character(N)` blank-padding)**, `TODO.md:919-921` — an on-disk
  PG-compat defect that *"shifts `relpages` on every `bpchar` table."*

Also open here: **K43** — no partial-Append producer, so there are **zero partial
paths on join rels** via that route; PG uses Parallel Append in 6 queries and only
Q5 + Q76 miss. And **K20**, a structural fact: `GOOPG_GATHER_PATHS` gates *partial
paths only, not parallelism* — the post-pass produces a Gather regardless, so
**there is no setting that yields a serial plan**. R13 named the possible fix as a
design question, not an edit.

## B4 — aggregation-strategy has no working lever

TPC-H **10**, TPC-DS **69**. Both attempted levers are dead:

- R120's currency correction makes TPC-DS **worse** (69→71) and cost TPC-H a match
  (Q10 lost). See F14.
- R124 §7 refuted the ncols-narrowing pairing by measurement.

The named lever is **K12(B) / K24 slice 3** — model the ordering requirement so
the upper planner can run PG's ordering contest. Per K24 it is the largest single
item in the workstream. It is **named and unscoped**, and two structural facts
bound it: K96 (the shape PG picks is unreachable by construction) and K97 (R45's
fix was rejected as architecturally impossible; the real item is PG's
`AGGSPLIT_INITIAL_SERIAL`/`FINAL_DESERIAL` — a multi-round **executor** programme).

Nearby and unretired: **O9 / K-note "#6"** — hash costing at large group counts
prefers GroupAgg+Sort where PG keeps HashAgg (TPC-H Q3 at ~307k groups: rows now
PG-close at 307,640 vs 308,817, **strategy diverges**), and column-ndistinct gaps
cap the lift (DS Q39 3,202 vs PG 7,823). R67 §3 legislated against carrying this
further without re-derivation; the carry continued anyway.

Also open: **O20** — the Q13-inner grouping admission gap. The hashed arm is
absent and GroupAgg is elected by default (goopg 350,574.23 vs PG HashAgg
65,167.54); *"Slice-2's Q13 MATCHED no longer holds"*, with R69's `nestloopCost`
the unproven prime suspect. Never scoped.

## B5 — PG's hash final-cost inputs are unobservable

The Q96 chain's terminal state, reached in five steps: R89 C2 named the missing
inputs (`inner_unique`, `outer_match_frac`, `match_count`, PG's virtual-bucket and
MCV-frequency values, PG-style `QualCost` startup/per-tuple split, join pathtarget
costs) → R90 and R91 landed partial ports → R96 STEP1 showed the walk formula
matches PG **per row** (0.00353 vs 0.00356) and the *level-2 runs* differ → **R98
ruled UNOBSERVABLE** (PG's live EXPLAIN does not expose them and no captured PG
statistics give a lawful derivation) → **R99 ruled ORACLE INVALID** (the only route
to observing them — running real PG 18.3 over a copy of goopg's data directory —
**crashed PG's parallel workers at `nbtsearch.c:707`, signal 6**, triggering crash
recovery).

R98's discipline is the correct one and should be preserved: *"Matching goopg's own
arithmetic is not evidence that PG's hidden inputs agree."* No cost change was
authorised; nothing was tuned, invented or forced.

Residues: **N39** — a ~12.5-row unexplained residual between goopg's Q96 L2
partial rows and the PG source-rule prediction, explicitly not masked (R88); and
Q96's remaining `[join-order, scan-type, qual-placement]` with **no measurement
path today**.

## B6 — No Memoize on the NL probe path R59 repriced

**Scope this precisely.** goopg *has* Memoize — `internal/executor/operators_memoize.go`,
`GOOPG_MEMOIZE` default-on, and a PG-faithful `MemoizePath` producer
(`internal/optimizer/joinpathsmemoize.go`, costed by `costMemoizeRescan`). R59's
finding is narrower and is quoted exactly: *"goopg's executor has no Memoize **on
this path**."*

R59 repriced index probes toward PG's constants — a correct, PG-faithful change —
and **DS Q72 went from 4s PASS to 320s TIMEOUT**, reproducible solo. The planner
moved *toward* the oracle; goopg's executor drowns in ~4M probes because it has no
Memoize on that path, where PG plans the same shape *with* one.

Carried unfixed through R60 ("now biting harder"), R61 and R62. The status channel
treats it as non-blocking by policy; the problem is real and unowned.

Its mirror image: R67 §2 notes goopg emits `Memoize` where **live PG emits zero
anywhere** in TPC-H — so goopg both over-uses Memoize where PG does not, and lacks
it where PG has it. Counted inside `parameterisation`.

## B7 — Should goopg reproduce PG's estimation errors?

The open strategic question, arrived at twice (F8): R79's ndistinct verdict (keep
the superior statistics) and R130's Q9 ground truth (actual 175; goopg 97 = 1.8×
low; **PG 60,125 = 344× high**).

R130's framing, recorded and not resolved: *"The goal accepts slower plans; it says
nothing about adopting PG's mistakes. That is a question for the goal's owner."*

It is load-bearing for Q9, for Q4 (whose needed 0.2317 selectivity derives from
PG's ndistinct **undercount**), and potentially for the whole join-order
programme. **No route past Q9 can be scoped until it is answered.**

## B8 — indexProbeCostMultiplier = 2.0: parity vs wall-clock

R58 §1.iv/§2: the multiplier deliberately departs from PG because goopg's executor
materialises the whole TID list eagerly. **At `mult = 1` the DP picks PG-shaped NL
plans that run 2–3× slower** (Q7 5.86s → 15.72s).

So *"closing the plan-parity gap means pricing probes at PG's constants while
goopg's executor still pays the eager-materialization cost."* The real fix — make
the executor cheaper, then lower the knob — is a cross-layer programme that **has
never been scoped**. K73 records the sibling item: retire `Join.FromOuterReduction`
when the NLI gate learns a real index-descent probe cost (capping by outer size was
rejected by measurement — TPC-H Q4 EXISTS 1.5s → 13.1s).

Note this interacts directly with B6: pricing probes PG-faithfully without Memoize
is how R59 produced the Q72 timeout.

## B9 — Broad executor performance gap

R97's survey, on pinned-GUC arms:

- **TPC-H SF1: goopg 68.8s vs PG 16.8s = 4.1×**, PG faster on 19/21. Worst
  ratios Q19 25×, Q21 20×, Q4 12×, Q9 9×; biggest absolute Q18 (21.04s vs 6.42s).
- **TPC-DS SF0.25**: totals goopg 170.2s vs PG 185.1s — *explicitly not a
  superiority claim*: **88 queries PG-faster, 8 goopg-faster**, and typical queries
  run **10–20× slower**. Worst: Q61 70×, Q58 26×, Q55 23×, Q88 21×, Q24 21×, Q80
  20×. The 8 goopg-faster cases are genuine PG-plan pathology on this sampled
  data, proven by a fair rematch at `work_mem=512MB`.
- **Config asymmetry audited and disclosed**: `shared_buffers` was not aligned on
  the DS lane (PG 2GB vs goopg 128MB) — *in PG's favour*, which strengthens the
  pathology reading rather than weakening it.

Prime suspect named: the row-width gap (B1). No owner round exists.

## B10 — correlation = 0 on every goopg index

`indexCorrelationFor` (`internal/optimizer/costindex.go:479-496`, guard at `:480-482`) returns 0 when the leading column
has no correlation slot, which prices **every index scan at `max_IO_cost`** — the
fully-random Mackert-Lohman bound (R58 §1.iv, root-causes §1).

Note a correction: **K27(a) established that correlation *persists* across restart
at HEAD** — the earlier "lost across restart" blocker is stale, and the bench heaps
simply predate the slot-3 writer. So this is an **operational gap** (re-ANALYZE)
plus a **fallback-pricing** gap, not a persistence bug. So what is open is
narrower than root-causes §7's priority-4 item as originally written — the
persistence half is closed (`TODO.md:952-957` marks it `[x] STALE 2026-09-09, no
code change (K27)`, the write/decode/restore chain being complete at HEAD).

Alongside it, **R30's residue**: ANALYZE never visits indexes, so
`estimateIndexGeometry` synthesises relpages / reltuples / tree_height. Derived,
not measured. Both shift index-probe pricing corpus-wide.

## B11 — The forced-shape route bypasses the cost seam

F10's consequence, stated as a live hazard: any investigation using forced
`join_collapse_limit=1` forms is measuring the **legacy/prebuilt constructor**, not
the path search — unless it first re-establishes which route the query takes.

This already voided R106's and R108's attribution. **There is no guard**: nothing
in the harness warns that a traced query fell to the legacy route, and R115 only
found it by running a positive control on the same binary and server.

## B12 — plan-gate is dead as a signal

R128 §5 corrected a false claim made in both R126 and R128 rev 1: `make plan-gate`
does **not** diff goopg against live PG. It is a **goopg-vs-committed-goopg
baseline pin** (`Makefile:431-453`, `cmd/plan-snapshot/main.go:293`).

So the 20/22 failure is not "expected until the goal is met" — it means **the
repo's own plan pins have not been re-baselined across ~125 rounds of intentional
plan change**. R128 verified the diverging *set* is identical OFF and ON, which is
the only reason the gate retains any value at all. It has been a standing opt-out
since R65 (R65 §2.7, R66 ×2, R69 §3.5, and R120–R124 consecutively).

Consequence: a genuine regression inside those 20 queries would currently be
invisible.

---

## C1 — ParamRef LIMIT + DISTINCT returns wrong rows

R83 fixed the `IntegerConst` case (Limit-below-Unique truncates pre-distinct rows,
wrong whenever duplicates exceed the limit) and pinned it with a synthetic
FAIL-before/PASS-after test. The `ParamRef` allowlist was **deliberately not
extended**, so *the values bug persists for that case*. Fail-closed and
zero-corpus-impact today — which is exactly why it will stay invisible.

## C2 — Values-green sweeps cannot detect qual-placement loss

Proved by R56 §5 (F20 item 1). **No gate was added**, and the class is open. The
specific remaining hole is named: `*Gather` crossing in `pushConjunctTraced` is
still deliberately excluded (O15), which is the same class as the Q78 defect.

## C3 — A second display/estimator seam, never root-caused

R76 P1 observed Q22 displaying rows 16,666 against a stamped 18,200 — *"a second
estimator seam (stamp applied to a node instance the renderer does not read?)"*.
R77 took the post-pass branch and noted *"gates did not complain"*. The seam itself
was never diagnosed. R61 records the same C-20a class for rewrite-time versus
EXPLAIN-time estimates, and K63 is the general form (see F11).

---

## Nuisances and smaller items

### Per-database catalog defects

- **N1.** `ALTER TABLE … DROP CONSTRAINT` on an FK **reports success and does
  nothing**: `InMemory.DropForeignKeyConstraint` hardcodes `DefaultDBOid`
  (`catalog.go:22241`) and `execAlterTableDropConstraint` discards the result
  (`operators_ddl.go:13275`). Pre-existing — and **R126 makes it worse in effect**,
  because such an FK now also survives restarts. It is why the step-(d) A/B had no
  same-epoch drop arm, so its per-category deltas are indicative, not pinned.
- **N2.** `HasPrimaryKey` (`catalog.go:22261`) has the same hardcoded-`DefaultDBOid`
  shape. Unverified but structurally identical.
- **N3.** **No in-process test crosses a DATABASE boundary** — `CREATE DATABASE` is
  a dispatch-layer statement the in-process parser rejects, so `pgConstraintTableRel`'s
  per-DB branch and the reload's `ListDatabases` loop (the exact paths TPC-H rides)
  have **manual psql evidence only**. Two per-DB defects were found in two rounds
  precisely here.
- **N4.** `pg_constraint` returns **0 rows of any contype after a restart**,
  including the `'p'`/`'u'` rows synthesised from indexes that demonstrably survive.
  A second, independent reload gap; R126 explicitly did not touch it.
- **N5.** `PhysicalTypeIsVarlena` (`physical_align.go:85-107`) has **no `IsArray`
  arm** — latent for ordinary user `int4[]` columns, not just catalogs.
- **N6.** `catalog.ForeignKey` stores an unschemed parent **name**, not an OID —
  makes `LookupTableByNameAnySchema` decline on cross-schema ambiguity, makes
  `fkParentRel`'s raw byte compare (`joinrelsize.go:760`) fragile, and lets a
  renamed parent go stale until a restart repairs it. One change retires all three.
- **N7.** `conpfeqop` / `conppeqop` / `conffeqop` stay NULL where PG populates them
  (deliberate; a standby would see the gap); and **N8**, no runtime `pg_constraint`
  index maintenance, so a standby reading via
  `pg_constraint_conrelid_contypid_conname_index` still sees no FK rows.
- **N9.** R126 P6 (restored FK OID not re-issued) is implemented but never
  exercised; no collision case was constructed.
- **N10.** `boundProven` / `rowsBound` are **misnamed** — they promise a proof the
  FK arm never delivers (PG derives no bound from `fkey_list`). R125 §3 recommends
  renaming, not restoring the guard.
- **N11.** Six `deleteCatalogRowsForOID` sites hardcode `DefaultDBOid` and were
  filed for confirmation; not done.

### Measurement instruments

- **N20.** **The K18 `$$` tempfile trap is still live.** `capture-tpcds.sh` embeds
  `$$` in its temp filename, and Q36/Q70/Q86's psql ERROR text carries the PID — so
  any two runs diff spuriously. It has produced false structural readings in
  **R122, R123, R124 and R128**. K18 was fixed at source once in 2026-09-08 and
  **regressed**. Currently worked around by stripping the PID, not fixed.
- **N21.** **Capture stamping gap.** Only the sweep artefact is machine-stamped
  with its flag arm; TPC-DS captures carry a **hand-typed** header and TPC-H
  captures carry **none at all** (R122 §10). This is the exact class of gap that
  produced R120's B1 — a flag-OFF run cited as ON evidence.
- **N22.** **TPC-DS `match=2` vs `match=1` is unreconciled** across two capture
  methodologies (R108/R113 vs R128). R128 verified `match=1` reproduces against all
  three in-tree references, so the number is methodology-dependent. The programme
  quotes both.
- **N23.** **Stats-epoch drift exceeds measured effects.** A values sweep
  re-samples statistics and opens a new epoch; R120 §6 attributed two apparent
  "worsenings" to drift, not the flag. R130 rev 1 measured same-code ANALYZE drift
  on Q9 at **1.31×** — a figure produced *against* R130 rev 1 as its refutation,
  from R128's three committed Q9 captures, not measured by rev 1 itself. Separately
  R63 found a **stale** clone's carried statistics at 201,356 against 196,849 from
  two **fresh** clones that agreed deterministically (~2.7%) — i.e. evidence that
  stale stats differ from fresh ones, not that identical inputs drift. Autovacuum
  moved Q3 by −8.62 in 70 minutes (R60). R120 left a standing rule
  (re-take the OFF baseline; all A/B numbers same-epoch) that is not enforced by
  any tool.
- **N24.** **`pg_statistic` is not queryable on goopg** (R87), so planner statistics
  cannot be read back through SQL — this blocks a whole class of A/B and is a real
  tooling blocker, not a nicety.
- **N25.** **Load-bearing prose figures that were never re-measured**: R125's FK
  loss-across-restart and the **32.17s / ~2-week** validation estimate, which is the
  input to "step (c) is optional". Flagged by R125 §7 and still outstanding at R126 §7.
- **N26.** **Reference oscillation**: live-PG TPC-H Q8 flips
  MISSING-NODE ↔ SHAPE-DIFF between same-day captures from PG-side stats drift
  (R66, R67, R69) — the oracle itself is not stable at the margins. Related: K9
  binds `bench/tpch/plans-pg/` as **not** a parity target (stale and serial), and
  the owner waiver for re-capturing that fixture was still pending at the last
  handover.
- **N27.** **Census reproducibility.** R122's census scaffolding was removed and its
  raw output never retained, so its figures existed only as prose; R123 rebuilt it
  and committed the raw dumps, establishing the norm; **R124 then partially
  regressed it** (dropped R123's MIXED detail fields, emitted no zero arms), and its
  MIXED=0 verification rests on `tmp/goopg-bench-bin`, which the bench scripts
  overwrite. K90's rule stands and is not enforced: anything needed across sessions
  must live in the repo.

### Flag and code debt

- **N16.** **`GOOPG_HASHAGG_WIDTH_CURRENCY` is marked for DELETION and has not been
  deleted** (R124 §7 resolved its promote-or-delete to *delete*; R128 did not do
  it). It ships default-OFF and net-negative.
- **N17.** Two further default-OFF cost arms accumulate with no expiry:
  `GOOPG_PG_HASH_TUPLE_SPILL_COST` (R108) and `GOOPG_PG_SORT_RELATION_BYTES_COST`
  (R113). Both reach real branch boundaries and cross none. With N16 that is
  **three** default-off cost arms at HEAD, not four — the fourth,
  `GOOPG_NARROW_COST_INPUTS`, was **promoted by R128** and now reads `unset(on)`.
  R121 §6 flagged that *"past about four they become debt"* and that the norm
  belongs in the take2 charter — **it was never moved there.** Also default-OFF
  and unlanded, per `scripts/planner-flags.env`: `GOOPG_GATHER_PATHS`,
  `GOOPG_ONEREL_SEARCH`, `GOOPG_PARTIAL_SORT_PATHS`, `GOOPG_HASH_OUTER_JOIN`.
- **N18.** **Stale prose now inverted by R128's promotion**:
  `joinpathsmemoize.go:256-267` reasons about the flag being off, and
  `narrowcostinputs_test.go:341-350` claims to protect "the DEFAULT arm" which is
  now the other arm. Filed, not fixed.
- **N19.** **R124's accepted planner-below-executor `avgVar` divergence is now
  shipped default behaviour** (R128 §4). Pinned by
  `TestR124AcceptedAvgVarDivergenceOnNonTableChild` and bounded by the SF=1
  execution pass, but the debt is **assumed, not discharged**. The underlying
  `avgVar = 0` under-statement for non-table leaves that really emit text is
  pre-existing on both arms and was not fixed.
- **N28.** `tmp/d05p2-bucket-charge.patch` is **stale** (3 rejected hunks), is 197
  lines across 5 files rather than the one-constant edit its name implies, and its
  `Choose` / `join_batch.publish` / `entrywidth.go` parts have **unmeasured
  interaction** with R128's now-default narrowing. Separately, `MapSlotBytes = 48`
  (`hashsize.go`) is documented as **known 2× low** (go1.25 measures 96.1 B for
  `map[string][]Row`), worth bucket heap 586.7 → 286.0 MB and −34.5% per-worker
  peak — and R129 measured it **parity-inert**, so it is a `minimize_datum` item,
  not a parity one.
- **N29.** `scripts/pg-regress-runner.sh` was **not run for R128**, despite upstream
  cases (`create_index`, `join`, `select_parallel`, `partition_prune`) pinning
  EXPLAIN text — exactly the class a planner cost-input change can move. Three
  cases also flap on an unchanged build.
- **N30.** `internal/parser` fails **60 tests**, pre-existing and verified unrelated
  to R126. Unowned.

### Estimator and cost residuals

- **N31.** **Search-side parallel rows are 4.000× inflated** relative to goopg's own
  display and to PG (uniform across Q5/Q7/Q8/Q9 = `mpwg=4`). R54 designed the fix
  and the round **failed back to design**; no part landed.
- **N32.** **Q7-class join-selectivity gap: goopg 229,626 vs PG 2,520 — 91×**, with
  the n1/n2 OR-clause side the prime suspect (R54).
- **N33.** **K31 — CTE inlining.** goopg materialises CTEs where PG inlines them
  (PG 12+ `inline_cte`): TPC-DS goopg **111 `CTE Scan`** vs PG **68**, with 40 of 63
  declarations (63%) single-reference and inlinable. Consequences: the
  `outer-over-derived` firewall fires, rows are synthesised where PG has real
  statistics, and the search sees one opaque rel. Structural, open.
- **N34.** **K43 — no partial-Append producer**; PG uses Parallel Append in 6
  queries, only Q5 + Q76 miss.
- **N35.** **K13** — `partialAggNotionalRows` substitutes a notional row count;
  with a memory threshold in `costAgg` it can flip a verdict a real row count would
  not. Needs `TableStats.RowCount` restored at startup.
- **N36.** **K56 — ANALYZE cannot see same-transaction rows** (a PG divergence):
  the visibility test rejects everything (`operators_analyze.go:873`) while
  `Pages=1` proves pages were read. Any test or tool that ANALYZEs inside a
  transaction reads zeros and **looks healthy**. K56 is recorded as deserving its
  own round; still open.
- **N37.** **K48 — three pre-existing `FoldConstants` defects**: numeric arithmetic
  via float64 (`1.10+2.20` → `3.3`), no int4 overflow (22003 not raised), byte-wise
  string ordering ignoring collation. Related **K88/K93**: folding *can* change
  answers (`0.05+0.01` → `0.060000000000000005`; an `interval 'infinity'` fold once
  returned a wrong timestamp, caught by the suite, not review). K93 correctly
  deprioritises it as a **fidelity** item, not a parity lever.
- **N38.** **K87(1) — no reachable `provolatile` index.** The constant fold stays
  scoped to one hand-verified immutable family; broadening requires generating a
  volatility map from `pg_proc.dat` (the optimizer cannot import initdb).
- **N39.** Q96's **~12.5-row unexplained residual** (R88), explicitly not masked.
- **N40.** **K95 — parallel row estimates diverge in both directions** and no single
  convention explains it (Q1 3.99× high, Q14 4.26× high, **Q6 0.05× = 19× low**) —
  at least two independent root causes on the same column of the same table.
  R44 also moved Q6's estimate *further* from PG.
- **N41.** **K41** — dimension-table `relpages` divergence unexplained (B3).
- **N42.** **K58** `scaleByFloat` truncates where PG's `clamp_row_est` rounds;
  **K59** goopg-only thresholds (`nliMaxOuterRowsHeuristic`, `memoizeMinOuterRows`)
  are crossed far more often after R36, so some shape changes are heuristic- rather
  than cost-driven; **K57** `inferAnchoredEqualities` rule (2) is vacuously true for
  default-priced filters and currently inert — "re-derive before wiring".
- **N43.** **K33** — TPC-DS Q68 `customer`: goopg prices a Bitmap Heap Scan below an
  `Index Scan using customer_pkey` where PG takes the index. First clean
  bitmap-vs-index costing divergence; isolated by R29, open.
- **N44.** **K63** — EXPLAIN reports a scan cost the planner did not use (4.5× on
  Q12). Does not affect plan choice; corrupts every cost-based artefact including
  `plan-gate MODE=semantic-cost` and estimate audits.
- **N45.** Ledger items carried across ten-plus rounds without retirement:
  **R55 §3 tie-break calibration; F3 procost; AGG_MIXED** (Hash-vs-GroupAgg at ~6k
  rows / 2 groups); **Q8's ~25k cost margin**; partial-skew ndistinct over-count
  (R63-#1); `soleBaseScan` / `IsSmallDimensionSide` / `outerScanRowCount` lacking
  IOS arms (R63-#2); `estimateAggregate` never firing on CTE-body joins (R61-#4);
  R62-#1's restricted+parameterised Yao corner; LowKey/HighKey/SAOPKeys probe
  detection not enumerated; `loopCount` 0-vs-1 unification; the NLI inner-cost
  EXPLAIN display stamper. R67 §3 diagnosed the pathology itself and legislated
  against the carry; the carry continued.
- **N46.** **O5/O17 residue** — partial-NL paths cannot complete to a Gather via
  `PartialPathlist[0]`-only reading, and NL-head eviction killed Gather completion
  on Q5/Q10 (R60). Partly mitigated by R94's INNER narrowing; not closed.
- **N47.** **`add_partial_path_precheck` is not mirrored** — planner CPU waste on
  astronomically-priced plain arms PG would bail on before costing (R60).
- **N48.** **K100** — sequential `reg*[]` comparison is broken twice over
  (scalar-cast leak drops `IsArray`; OID-vs-name compare without a catalog). R46's
  carve-out keeps the index there. Corpus impact zero. And **K40** —
  `round(double precision, int)` resolves on goopg but does not exist in PG 18.3.
- **N49.** **O25** — `M0129-S1` never returns `costsEqual`, diverging from PG's
  `COSTS_EQUAL → pathkeys decide`. Recorded by R81 as hygiene and explicitly not
  proposed; not load-bearing for Q4, but it is a real behavioural divergence in the
  comparator that decides elections.
- **N50.** **O1–O4, O7, O30–O31** — redundant synth clauses have visible cost and
  "the DP still minimises" is an **unverified null hypothesis** (no edge-admission
  audit was ever run); synth nodes reuse original `*ColumnRef` pointers; the
  nullable-side closure-input guarantee lives only in comments with no assertion
  test; Q31's synth copies render base-table-qualified Vars above a CTE boundary;
  and the split re-derivation term audit was never done. (Two residues recorded
  in the round reports — `q15a.sql`'s missing EXPLAIN prefix and the untracked
  `internal/executor/zz_probe_r66_test.go` — **no longer exist in the tree at
  HEAD** and are listed here only so a reader does not go looking for them.)
- **N51.** **K67's residue is the hard half of B1** and deserves its own line:
  even with perfect column pruning goopg is 72 B/row → 103 MB and **still spills**
  at `work_mem=64MB` where PG is 22 B/row → 31 MB.
- **N52.** **`tools/tpcds_ri.sql` is referenced by nothing** under `bench/tpcds/`,
  `scripts/` or the `Makefile`, so **TPC-DS can never carry an FK on the current
  build path** (R125 §4). Anyone scoping FK work must size against TPC-H's 14
  join-order queries alone.
- **N54.** **Absorbed-leaf `index_qual_cost` needs its own design.** Filed by
  R21 as its one genuinely open residue and not carried anywhere else:
  *"Filter-chain counting cannot reach it by construction"* (`TODO.md:929-931`).
  Rule-based index choices carry absorbed bounds, not Filter chains, so the
  census that declined R21 cannot see this case either.
- **N53.** **O28 — the `Materialize` producer stays correctly queued.** Its
  re-triage condition (a matching query that needs it) is concrete and currently
  unmet: live PG uses `Materialize` once in TPC-H and goopg prints zero, and a
  36-site NL-inner census found no coinciding site (R67).
