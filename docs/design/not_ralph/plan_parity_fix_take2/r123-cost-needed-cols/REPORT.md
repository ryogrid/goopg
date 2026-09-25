# R123 result — the 42,679 mixed pairs are ONE cause, 100%: NON-TABLE LEAVES (CTEScan 14, Project 9, SetOp 7, Filter 2) carry no per-column stats. Decision-table row 2 fires.

Measurement-only. **Zero Go changes ship** (`git diff` over `internal/`,
`cmd/`, `scripts/` is empty), instrumentation removed, raw dumps
retained.

The census is unambiguous: **100% of TPC-DS's 42,679 mixed-currency join
pairs trace to a single cause** — 32 non-table leaves that carry no
per-column statistics and therefore decline to narrow, each poisoning
every join above it. Rev 1's collector hypothesis was refuted before
implementation and the census confirms it; the review's
non-whitelisted-wrapper hypothesis (Append/SetOp/HashAgg wrappers) finds
**zero** instances. The pre-registered **decision-table row 2 fires**.

## 1. The finding

| bucket | TPC-DS SF0.25 | TPC-H |
|---|---|---|
| JOIN-NARROW | 73,349 | 4,257 |
| **MIXED** | **42,679** | **3** |
| BOTH-UNNARROWED | 8,588 | 98 |
| REL-NARROW | 590 | 65 |
| REL-DECLINE | 176 | 4 |
| **mixed share** | **34.2%** | **0.07%** |

Root-cause attribution of the 42,679 (walking past `childCascade` to the
first real reason):

```
42,679  root = nonWhitelistedKind : PathKind 0   ← 100.0%
```

`PathKind 0` is **`PathPrebuilt`** — the wrapper around an
already-constructed executor Node. Rel-decline arms:

```
144  arm=a  collector declined
 32  arm=c  nil ColVarBytes
```

Host join kinds: NestLoop 17,150 / MergeJoin 15,739 / HashJoin 9,790.
Sides: outer 23,025 / inner 19,654.

**TPC-H's 3 mixed pairs root at `wrapperGap-indexOnlyChild`, not at
`PathPrebuilt`** — a *second*, distinct mechanism (rule 4 refusing to
launder an index-only triple onto a wrapper, R122's own fix). It is
tiny, but it sits in the corpus this chain actually gains on, so R124
should not assume the TPC-DS cause is the only one.

The TPC-DS captures cover **96 of 99** queries: Q36/Q70/Q86 fail to
parse on both engines and contribute 3 `ERROR` lines, unchanged across
arms.

## 2. The mechanism, and why both hypotheses missed it

goopg's search does not keep a sub-problem's pathlist alive across the
boundary. A CTE body, a derived table or a FROM-subquery is planned by
its own `planSelect`, and its **cheapest tree is republished as a NODE**,
which the enclosing problem admits as **one `PathPrebuilt` initial rel**
(the design is stated in `relfromjoinlist.go`'s header; the seed is built
at `joinsearch.go:434`).

Such a rel has `ri.table == nil`, so `rel.ColVarBytes` is never populated
(`joinsearch.go:403-411` guards on `ri.table != nil && ri.table.Stats
!= nil`), so `relNarrowedWidths` declines it on **arm (c)**. Its
`PathPrebuilt` therefore publishes `NCols == 0`, and **every join pairing
it with a narrowed sibling is a mixed pair** — at every level above it.

**Just 32 declining rels produce all 42,679 mixed comparisons**, because
one un-narrowed leaf appears in a combinatorial number of DP pairings.
(That inference is sound because the buckets are disjoint: an arm-(a)
search has no narrowed rel at all, so its joins can only land in
BOTH-UNNARROWED, and arms (b)/(d)/(e) are zero — so every mixed pair's
un-narrowed leaf sits on an arm-(c) rel.)

### Which 32 rels — MEASURED, not asserted

The first draft asserted "sub-problem results" as fact. Review correctly
objected that arm (c) fires for three indistinguishable populations,
including **an ordinary table with no usable stats**
(`joinsearch.go:403` guards on `ri.table != nil && ri.table.Stats != nil
&& len(Stats.Columns) > 0`). So it was measured:

| declining leaf | count |
|---|---|
| `*optimizer.CTEScan` | **14** |
| `*optimizer.Project` (planned sub-problem subtree) | **9** |
| `*optimizer.SetOp` | **7** |
| `*optimizer.Filter` | **2** |
| **ordinary table lacking stats** | **0** |

All 32 are **non-table leaves**; not one is a table. So the population
is exactly the one the pre-registered decision table's row 2 names —
"non-table leaves (CTE/subquery/VALUES) carry no per-column stats" —
and it is broader than the first draft's "sub-problem results": a
`CTEScan` is the single largest group.

Why each hypothesis missed:

- **Rev 1 (collector declines)** was refuted by arithmetic before
  implementation and the census confirms the refutation exactly: the 144
  arm-(a) declines are search-problem-uniform, so their joins land in
  **BOTH-UNNARROWED (8,588)** and contribute **zero** mixed pairs. The
  pre-registered falsifier — *"statement-gate declines appearing in the
  mixed histogram at all"* — **did not fire**.
- **The review's hypothesis** (`PathSetOp`/Append/SubqueryScan/HashAgg
  publishing `NCols == 0` beside a narrowed sibling) had the right
  *shape* — a non-whitelisted kind on one side — but named the wrong
  kinds. Not one mixed pair roots at a set-op or aggregate node; every
  single one roots at `PathPrebuilt`. The reasoning was sound and the
  measurement still overturned it, which is the point of running it.

## 3. Admitted-on-arrival share — 59.6%, an UPPER BOUND on the decisive share

```
25,450 / 42,679  =  59.6%  accepted on arrival
17,229 / 42,679  =  40.4%  dominated on arrival
```

**This is not literally the figure the SCOPE pre-registered, and the
substitution matters.** SCOPE §3.5 asked for mixed comparisons where
`addPath` actually **evicted** the other candidate. What was measured is
`pathlistVerdict` (`path.go:947`), which returns `verdictAccepted` iff
the new path is the tail of the resulting list — i.e. it **survived on
arrival**. Two consequences:

- Accepted ⊋ evicting. A path that evicts nothing — the first path on a
  rel, or one incomparable on every axis — still counts as accepted. So
  **59.6% is an upper bound** on the decisive share, not the share.
- Acceptance is evaluated at insert time, and `addToPathlist` prunes
  dominated incumbents on every *subsequent* insert, so an accepted path
  can be evicted later. The census does not track final pathlist
  membership, so "a path the search kept" — which the first draft
  claimed — is **not** what was measured.

The directional conclusion still holds at this magnitude: an upper bound
of 59.6% is nowhere near zero, so the "confound never moved a plan" row
does not fire. But the true decisive share is unmeasured and is
somewhere in (0, 59.6%].

## 4. Counter validation and gates

- **TPC-H sanity gate (PASS).** The reimplemented counter reproduces
  R122's TPC-H figures exactly — 4,257 / 3 / 98 / 65 / 4, i.e. the same
  **0.07%** mixed share. A counter that could not reproduce a known
  near-zero was not to be trusted on TPC-DS; this one can.
- **Totals match R122 on TPC-DS too** (73,349 / 42,679 / 8,588 / 590 /
  176), which is a second independent check that the bucket definitions
  did not drift despite the reimplementation R122's review warned about.
- **Instrumented build is plan-identical to a clean ON capture** (PASS).
  TPC-H is byte-identical; TPC-DS differs on 8 lines that are entirely
  capture-harness noise (the `#` header label and the psql tempfile name
  inside the 3 unplannable queries' ERROR text — the K18 trap again).
  So the gate's substance holds: no path selection was perturbed.
- **Zero Go changes** at commit, proved literally by an empty
  `git diff` over `internal/`/`cmd/`/`scripts/`, not by a grep.
- Suites green. No values gate: nothing ships.
- `make plan-gate`: not applicable — nothing ships and the flag is
  default-off.

**Denominator definition** (R122 left this undefined): a "join costing"
is one `narrowJoinWidths` invocation, i.e. one candidate join path
offered to `addPath`/`addPartialPath`. **124,616** on TPC-DS SF0.25
(73,349 + 42,679 + 8,588) and **4,358** on TPC-H (4,257 + 3 + 98), at
the pinned epoch below.

**Correction:** the first draft wrote 4,364 for TPC-H, which was
R122's number borrowed rather than measured. R122's total included a
sixth bucket — `joins declined, index-only child (rule 4) = 6` — that
**this round's counter never emitted**, so R123's TPC-H denominator is
4,358 and the two are NOT bucket-for-bucket identical. The mixed share
is unaffected (3/4,358 = 0.069%, still 0.07%), but "reproduces R122's
figures exactly" was an overstatement: it reproduces **five of six**
buckets and silently dropped the sixth. SCOPE §3.3 required the
zero-valued arms be carried explicitly for exactly this reason.

**Epoch:** TPC-DS SF0.25 on :65437, `work_mem` per the capture script,
no restart or re-ANALYZE between the census runs. R122's 34.2% is cited
as a prior reading only; that it reproduces here is a bonus, not the
basis of any claim.

## 5. Not measured, and saying so

- **Per-query spread.** The SCOPE asked for mixed pairs bucketed by
  query. Not done: the capture runs all 99 queries against one server
  and the census stream carries no query boundary. It matters less than
  it would have with a mixed distribution — the cause is 100% single —
  but "32 rels" is a rel count, not a query count, and how those 32
  spread across the 99 queries is unknown.
- **SCOPE §3.2 — the per-gate-term decline counter — was NOT built.**
  The SCOPE specified it at length, including the top-level-vs-nested
  re-entrancy handling. The dumps carry one undifferentiated
  `arm=a-collector` line instead. It is the measurement R122 originally
  asked for and it is still outstanding; it bears only on the 6.9%
  arm-(a) population, not on this round's finding, but it was dropped
  without disclosure in the first draft.
- **SCOPE §3.1 required BOTH histograms** — the raw per-`Kind` one "which
  makes the cascade visible" *and* the root-attributed one. Only the
  root-attributed one was emitted, so the cascade is inferred rather
  than shown.
- **Rule-4 (index-only child) declines were never counted** on either
  corpus — see the denominator correction in §4.
- **Per-query spread.** Not done: the capture runs all 99 queries against
  one server and the census stream carries no query boundary. A single
  root cause is still compatible with most of the 42,679 coming from one
  or two wide queries (q64/q14-class), so "how concentrated" is unknown.

Gate commands actually run: `go test ./internal/optimizer/` (ok) and
`git diff --name-only -- internal/ cmd/ scripts/` (empty).

## 6. Verdict against the pre-registered decision table

**Row 2 fires: "largest root cause = arm (c) nil `ColVarBytes`" →
R124 = stats round, lever = non-table leaves.** The measured identity
(§2) matches row 2's wording exactly.

The first draft claimed no row matched and proposed a narrower R124
("carry narrowed widths across the sub-problem boundary"). Review showed
that escape rested on a **taxonomy artefact of my own instrumentation**:
`narrowJoinWidths`'s whitelist is the *join* whitelist, so a base-rel
scan path can never be in it, and tagging a declining leaf
`nonWhitelistedKind` is uninformative **by construction** — it pre-empted
the `relDeclineArmC` tag the SCOPE had defined for exactly this case. So
row 1 provably does NOT fire (zero mixed pairs root at a set-op or
aggregate *wrapper*), and the "composite" was an artefact, not a finding.

The narrower proposal would also have missed most of the population: it
covers planned sub-problem subtrees (the 9 `Project`s) but reaches
neither the 14 `CTEScan`s nor the 7 `SetOp`s, which together are 66%.

**R124 = give non-table leaves a per-column width basis** so
`relNarrowedWidths` can narrow them instead of declining. Constraints
already established:

- The seed is created at `joinsearch.go:434` (`newPrebuiltPath`) and the
  base-rel sweep runs at `relfromjoinlist.go:723` (after
  `addBaseRelPartialPaths`/`addBaseRelIndexPaths`, before
  `addBaseRelGatherPaths`), so the sweep is the natural place to stamp a
  width — no new call site. A `PathPrebuilt` on a *narrowing* rel cannot
  be missed by it (it sweeps both `Pathlist` and `PartialPathlist` and
  skips only `NCols > 0`), which is why C(iii) is excluded.
- For a **sub-problem** leaf the width is already known internally — its
  own search narrowed its own rels. For a **CTEScan** or **SetOp** it is
  not, and inventing one is forbidden; the honest options are to derive
  it from the leaf Node's own output schema plus whatever per-column
  statistic the body can supply, or to keep declining. R124 must not
  assume the sub-problem answer generalises to all 32.
- It must respect A(iii): all-or-none per rel.
- **It must not fabricate a width.** If the sub-problem itself declined
  (its own collector or stats), the enclosing rel must keep declining.
- The 144 arm-(a) collector declines remain untouched and remain worth
  ~6.9% of join costings; they are a separate, smaller lever, and rev 1's
  withdrawn Slice D is where that work would resume — subject to the
  **B4 inversion hazard** recorded in the SCOPE §6.

## 7. Honest sizing — should this continue?

The SCOPE permitted "not worth finishing for TPC-DS" and it is still a
live answer, so here is the case both ways.

**For continuing:** the confound is now a single, named, mechanically
simple cause with a decisive share of 59.6%. That is the best-understood
blocker this workstream has had, and R122 showed that when the currency
IS uniform (TPC-H, 0.07%) the chain produces real PG-faithful movement.

**Against:** even a perfect fix only makes TPC-DS's categories
*readable*. R122's ceiling stands — TPC-DS's dominant categories are
`join-order` (90) and `parallelism` (86), which this chain does not
touch — so the realistic upside remains a subset of `join-method` (62)
and `scan-type` (57), and no TPC-DS query is close to a MATCH.

**Recommendation:** do R124, because it is small and it is the
difference between "we don't know" and "we know", then decide promotion
on both corpora with readable evidence. Do **not** start a
`join-order`/`parallelism` programme on the strength of this chain.

## 8. Artefacts

Raw census dumps are **committed into this round's directory**, which is
the deliverable R122 failed to leave (its figures were consequently
unreproducible, and the first draft of this REPORT repeated the mistake
by citing `/tmp` paths):

- `census-tpcds.raw.gz` — the full TPC-DS SF0.25 census stream
- `census-tpch.raw` — the TPC-H counter-validation stream
- `rel-identity.txt` — the arm-(c) declining-leaf histogram behind §2

Plan captures (tmp only): `ds-r123c3.plans.txt`, `ds-r123id.plans.txt`,
`r123tpch.plans.txt`.
