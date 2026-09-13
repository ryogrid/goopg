# R121 Slice A result — narrowing works mechanically and moves real plans, but is PARITY-NEUTRAL at this slice. Keep default-off; the pairing question moves to Slice B/C.

Slice A landed as specified and is **measurably doing its job**: it
narrows base-rel cost inputs by 21 of 37 columns on TPC-H Q10's four
relations and changes real join orders on TPC-DS. It moves **no parity
metric on either corpus**. Flag stays **default-off**.

## 1. What was implemented

`GOOPG_NARROW_COST_INPUTS` (strict `== "1"`, default-off,
provenance-registered), in `internal/optimizer/narrowcostinputs.go`:

- **A(i)** — `narrowBaseRelCostWidths()`, one sweep over the base-rel
  level after every scan producer has run (`relfromjoinlist.go`, after
  `addBaseRelIndexPaths`, before `addBaseRelGatherPaths`). A sweep
  rather than five constructor edits: it cannot miss a producer (the
  prebuilt SeqScan is built ~30 lines *before* the needed-column stamp
  and can never narrow at its own constructor; the partial-seqscan,
  index, bitmap and parameterised-index producers each build fresh
  `Path` values that set no triple), and it makes the all-or-none rule
  hold by construction rather than by five call sites agreeing.
- **A(ii)** — `inheritNarrowedWidths()` on the single-child wrappers
  that project nothing: Gather, GatherMerge, Sort, Memoize. Plus
  `costMemoizeRescan` switched from a **direct** `relNCols(innerPath.Rel)`
  read to `pathNCols(innerPath)` — it could never have seen narrowing
  otherwise. (There is deliberately no `PathMaterial` kind; the scope's
  first draft listed one in error.)
- **A(iii)** — all-or-none **per rel**, among the paths this round
  writes, with index-only paths exempt (their `covered` set is a
  genuinely narrower *emitted* schema). Without this, `addPath` would
  compare sibling candidates costed in different currencies.
  **A leak here was found in review and fixed**: index-only paths carry
  `NCols` unconditionally, so A(ii) would have laundered that triple
  onto a Gather over a rel that *declined* to narrow, while the sibling
  Gather over its partial SeqScan carried none — two Gathers on one rel
  in different currencies, i.e. the same bias re-entering through the
  wrapper instead of the scan. `inheritNarrowedWidths` now refuses an
  index-only child, confining that narrowing to the scan level
  (pinned: `TestInheritNarrowedWidthsRefusesIndexOnlyChild`).

Narrowing goes on the **Path**, never the rel: `RelOptInfo.AvgVarBytes`
/`ColVarBytes` are `buildAvgVarBytes`'s deliberate over-charge decline
for the **executor's** hash entry, and rels are per-relset singletons
shared across candidates.

**15 pins**, including the end-to-end one the SCOPE mandated. Rev 1 of
this REPORT claimed that coverage while every pin in fact built
`searchCtx{joinrels: …}` by hand — reproducing verbatim the failure mode
the SCOPE warned about, and leaving nothing that would notice if the
production call site were deleted. Fixed: `TestNarrowCostInputsReaches
TheLiveSearch` plans a real statement through `PlanWithSettings` and
asserts a searched join cost moves between arms. It was **mutation-
tested** — commenting out `s.narrowBaseRelCostWidths()` makes it fail
with `off=[2.8057873713864833e+06] on=[…same…]`, so it is not vacuous.

## 2. It fires, and by how much (P4)

Measured with a temporary dump (since removed), TPC-H Q10's four base
relations:

| relation | leaf cols | narrowed | avgVar |
|---|---|---|---|
| lineitem | 16 | **4** | 46.0 → 1.0 |
| orders | 9 | **3** | 74.3 → 0.0 |
| customer | 8 | **7** | 137.9 → 128.8 |
| nation | 4 | **2** | 72.5 → 7.1 |
| **total** | **37** | **16** | |

The sweep stamps real paths in the live search (6, 5, 3 and 3 paths on
the four rels). So P4's mechanism is confirmed: 37 → 16 columns, against
the ~7 PG carries.

`orders`' `avgVar 74.3 → 0.0` is a genuine zero, not a silent decline:
`relNarrowedWidths` distinguishes an ABSENT `ColVarBytes` entry (which
declines the whole rel) from a present zero, so a 0.0 total means the
three kept `orders` columns are all fixed-width — which they are
(`o_orderkey`, `o_custkey`, `o_orderdate`).

## 3. Results

| # | bar | result | verdict |
|---|---|---|---|
| P0 | OFF bit-identical to pre-round HEAD | TPC-H captures **byte-identical** | **PASS** |
| P1 | narrowed values non-increasing; all-three-or-none; per-rel all-or-none; nothing set where the collector declined; **plus a per-corpus census of declined rels and narrowed-vs-declined join pairs** | invariants pinned by 15 tests incl. the full decline matrix (collector declined / nil set / empty keep-set / no `ColVarBytes` / unattributed column). **The census clause was NOT run** — unit tests cannot produce one | **PARTIAL** — invariants PASS, census unmeasured (see §6) |
| P2 | values unchanged ON | SF0.25 sweep stamped `GOOPG_NARROW_COST_INPUTS=1`: **PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0 SKIP=3**. Spotcheck Q12=2/Q13=34 PASS | **PASS** |
| P3 | no parity regression; join-order / join-method / sort-strategy non-increasing | **TPC-H unchanged: 6/15/0/1/0, all categories identical, ON vs OFF plan diff = 0 lines.** **TPC-DS unchanged: match=2, and every category identical** (join-order 90, join-method 62, scan-type 57, parameterisation 48, aggregation-strategy 69, sort-strategy 76, parallelism 86, qual-placement 17, rendering 20) | **PASS (nothing regressed) — but nothing improved either** |
| P4 | build-side `pathNCols` drops toward the needed set | 37 → 16 columns (§2) | **PASS** |

Suites green, `go vet` clean.

P3's `sort-strategy` clause passed **trivially, not meaningfully**. The
SCOPE added it because goopg's Sort does not project, so a narrowed
scan under a Sort under-charges a sort that really runs at full width.
With TPC-H byte-identical and TPC-DS categories frozen, that hazard was
never exercised — it is **deferred, not discharged**. A later round must
not read `sort-strategy=76` unchanged as evidence the under-charge is
harmless.

## 4. The finding: mechanically correct, parity-inert at this slice

- **TPC-H: literally zero movement.** ON vs OFF plan text is
  byte-identical *including every cost*. The narrowed inputs reach
  `hashsize.Choose`, but at the pinned 128 MiB budget nothing on this
  corpus appears to sit near a batch/spill boundary, so the geometry —
  and therefore the cost — is unchanged whether the build row is 16
  columns or 4. **This explanation is inferred, not measured**: no
  `hashsize.Choose` input/output pair was captured. It is also the null
  hypothesis Slice B must beat, so Slice B should dump the chosen
  geometry at both widths and convert it from a story into a fact.
- **TPC-DS: real plans move, parity does not.** 152 diff lines, and the
  shape-normalised diff shows genuine join-order changes (e.g. Q's
  `income_band`/`store` reordering, a `Join Filter` migrating up a
  level). Yet match stays 2 and **every** category is identical: the
  queries whose shapes moved were already SHAPE-DIFF and stayed
  SHAPE-DIFF with the same category set. Narrowing traded one
  non-PG-matching join order for another.

So Slice A is a **correct enabling change with no parity yield of its
own**. That is a real result, not a disappointment: it is the
arithmetic the roadmap has predicted all along — no query matches until
*all* of its categories close, and this slice closes none.

## 5. Why, and what actually follows

Slice A's reach is bounded by design and the bound is now measured:

1. **The join path's own width is untouched.** With Slice B deferred, a
   join path never sets `Path.NCols`, so `pathNCols` falls back to
   `relNCols(joinrel)` = the full sum of both inputs
   (`joinsearchlevel.go:632-643`). Only joins whose children are base
   scans or A(ii) wrappers see anything narrowed — one level.
2. **The aggregate is unreachable from here at all**, as R121's scope
   established before implementation: `aggInputWidth` reads the built
   node's schema, and the search root must publish the full binding
   concatenation. Q10's `ncols=37`/`avgVar=2080` are untouched, exactly
   as predicted — which is why P5 was withdrawn rather than run.

The 17x entry-size gap R120 identified therefore survives Slice A
intact at the aggregate, and mostly intact above the first join level.

## 6. Disposition

**Keep `GOOPG_NARROW_COST_INPUTS` default-off.** Promoting a change that
moves TPC-DS join orders while improving no parity metric would be
churn: it trades one non-matching shape for another and re-baselines
every plan pin for nothing. The code is retained because it is correct,
pinned, and is the prerequisite Slice B needs — Slice B has no narrowed
children to sum without it.

Do **not** tune anything to manufacture a yield here.

**Expiry — and R121 is NOT the same kind of thing as the others.**
R108, R113 and R120 are *candidate cost models* held off pending
evidence. R121 is *enabling infrastructure with no consumer yet*:
Slice B cannot sum narrowed children that do not exist. Folding it into
the same "four default-off cost arms" count flattens that distinction.
Its honest gate is therefore: **if Slice B does not land in the next
round-cluster, R121 is dead code and gets deleted** — not R120's
Slice C clock.

The four-arm observation still stands as a norm worth keeping (R108,
R113, R120 + this): default-off arms are evidence, but past about four
they become debt. That belongs in the take2 charter rather than buried
in a round REPORT, and is flagged here for someone to move.

**Next (Slice B):** sum narrowed child triples into the join path's own
`NCols`/`AvgVarBytes`/`OutputWidth`. Notes carried from R121's review:
key on `jt parser.JoinType` (`pathgen.go:78`, `:148`), not
`joinPublishesInner` which is not in scope at those constructors;
`JoinRight` publishes **both** sides, so outer-only is `JoinSemi`/
`JoinAnti` only; and Slice B must set `OutputWidth` too or it
re-creates R120's two-currency defect one level up.

**Residual to count in Slice B (from R121's review, not yet measured):**
A(iii) buys per-*rel* uniformity, not per-*comparison* uniformity. At
joinrel A ⋈ B where A narrows and B declines, the two hash orientations
are priced in different currencies, biasing toward hashing the
narrowable side. Slice B should census how many join pairs are in that
state before reading its own category deltas.

## 7. Artefacts

`/tmp/pp2-r120/`: `r121off.plans.txt` / `r121on.plans.txt` (TPC-H A/B,
byte-identical), `ds-r121off.plans.txt` / `ds-r121on.plans.txt` (TPC-DS
A/B, 152 cost/shape lines, zero category movement),
`r120.pg.plans.txt` / `pg-tpcds.plans.txt` (PG references).
Sweep with the arm stamped: `sweep-20260914-023437.txt`
(`GOOPG_NARROW_COST_INPUTS=1`).

**Capture stamping gap (review note):** the sweep artefact carries a
machine-generated `planner-flags:` line, but the parity captures do not
— `ds-r121on.plans.txt`'s arm rests on a hand-typed `# R121 ON` header,
and the TPC-H pair is byte-identical to itself so the label is the only
distinguishing evidence. `capture-tpcds.sh`/`capture-tpch.sh` should
adopt the sweep's stamp line. Not fixed this round; recorded.

`make plan-gate` not run — **reasoned omission**: the flag is
default-off and P0 establishes the default path is bit-identical to
pre-round HEAD, so the gate's structural pins cannot have moved.
