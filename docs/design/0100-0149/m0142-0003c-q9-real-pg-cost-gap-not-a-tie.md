# M0142-0003c — recon: is Q9's L6 divergence really an enumeration-order/tie-break question?

Status: accepted (landed 2026-09-16, recon only, no production code changed)

## Task

Per `.ralph/fix_plan.md`'s M0142-0003c line (filed by -0003b's finding): does
goopg's level-6 relset-pair generation loop visit candidate pairs in a
different order than PG's `join_search_one_level`, and if reordered to match
PG's, would that flip which of the two exactly-tied L6 candidates (goopg's own
hash-chain vs PG's chain's `nestloop.index`) gets registered first and wins
Q9's dominance tie? -0003b's Verdict had framed the fork this way: "the
tie-break mechanism is itself correct, PG-faithful behavior... the load-bearing
divergence is therefore which partition gets registered first at level 6."
-0003c was filed specifically to test that framing before attempting any
enumeration-order surgery (K50: reordering can flip queries that already
match). Measurement-only per `AGENT.md` §"Plan-parity harness" — no production
code changed; two read-only `EXPLAIN`s against the existing PG TPC-H reference
cluster (`:65432`, never restarted) plus reading already-committed trace
artefacts from -0003a/-0003b (no goopg server run this task).

## Method

**goopg side**: read the already-committed DPTRACE evidence from -0003a/-0003b
(`analysis/m0142/m0142-0003a-q9-dptrace.txt`,
`analysis/m0142/m0142-0003b-q9-dptrace-l6.txt`) — no new goopg run. Also read
`internal/optimizer/joinsearchlevel.go`/`joinsearch.go` in full: this is
already an extensively-cited, line-by-line structural port of PG's
`join_search_one_level` (phase 1 left/right-sided, phase 2 bushy, phase 3
last-ditch, the level==2 mirror-image dedup offset, `joinrels[1]` in FROM
order) — see the file's own P5.3/P5.3a design citations against
`joinrels.c:73-256` and the 03 §7 pair-count completeness test. Read to confirm
whether the phase/loop STRUCTURE genuinely differs from PG's (the fork's first
branch) before assuming it does.

**PG side**: two read-only `EXPLAIN`s against the always-on shared TPC-H
reference cluster (`postgres/local_install/bin/psql -p 65432 -d tpch`, never
restarted, no data touched): (1) Q9's exact SQL, serial
(`max_parallel_workers_per_gather = 0`, matching -0003a/-0003b's convention),
default planner order; (2) the identical query with `join_collapse_limit = 1`
+ `from_collapse_limit = 1` and the FROM list rewritten as explicit left-deep
`JOIN ... ON` in goopg's own chosen order (`part JOIN lineitem JOIN supplier
JOIN partsupp JOIN orders JOIN nation` — nation last, matching -0003a's
`{part,supplier,lineitem,partsupp,orders} ⋈ {nation}` partition) — the same
force-and-compare method M0142-0015 used for TPC-DS Q45. This fixes the JOIN
ORDER only; PG still freely chooses join method and access path within it, so
the comparison is apples-to-apples with the unforced run. Evidence:
`analysis/m0142/m0142-0003c-pg-{default,serial-default,forced-goopg-order}.txt`.

## Finding 1 — goopg's enumeration STRUCTURE is already a faithful port; the fork's first branch is closed

`joinsearchlevel.go`'s `joinSearchOneLevel` reproduces PG's phase 1 (`prev :=
s.levelRels(lev-1)` paired against `initial := s.levelRels(1)`, with the
identical level==2 `first = i+1` mirror-image offset), phase 2 (bushy,
`k`-loop to the halfway point, the `k == otherLevel` mirror offset), and phase
3 (last-ditch) — each block carries a `joinrels.c:<line>` citation and the
file states the construction was verified against PG's own closed-form pair
count (03 §7). `buildInitialRels` stamps relid `1<<i` from FROM-list position
`i` (`joinsearch.go:387-394`), i.e. `joinrels[1]` is FROM order, exactly PG's
`initial_rels`. For Q9's flat six-relation comma FROM list, both engines see
the same syntactic order (`part, supplier, lineitem, partsupp, orders,
nation`), so there is no divergent-initial-order explanation either. **The
loop nesting, dedup offsets, and initial-rel ordering that could make one
engine visit a pair "before" the other are, by construction and by an existing
completeness test, already identical.** This closes the branch of the fork
that asked "does the visitation order structurally differ" — it does not.

## Finding 2 — real PG does NOT see a tie at all; it sees a 64% cost gap

This is the decisive result and it overturns -0003b's Verdict's premise.
Real PG 18.3, forced into goopg's own chosen join order, costs **64% higher**
than its own default order — not a near-tie:

| plan | order | total cost (serial) |
|---|---|---|
| PG default | `{part,partsupp,supplier,nation}` → `+lineitem` (NLI) → `+orders` (hash) — PG's chain | **204932.03** |
| PG forced into goopg's order | `part` → `+lineitem` → `+supplier` → `+partsupp` → `+orders` → `+nation` (hash chain throughout) | **336207.55** |

Real PG's own chain wins outright, by a huge margin, in PG's own cost model —
it needs no tie-break at all. The forced plan's shape shows exactly why:
goopg's order never gets to exploit `lineitem_part_supp_fkidx` (the
`(l_partkey, l_suppkey)`-keyed index PG's default plan drives via a Nested
Loop **after** the four small dimensions — `part`⋈`partsupp`⋈`supplier`⋈`nation`
— have collapsed to only 72728 rows), because goopg's order joins `lineitem`
in as a hash-join **base** against `part` first, forcing a full 5,999,098-row
`Seq Scan on lineitem` (`cost=0.00..189336.98` alone, versus the whole
default-order NLI step's `cost=7378.27..125204.04`). Two genuinely different,
non-tied per-step economics in PG — not a coincidence, not a rounding
artifact.

**goopg's own cost model, however, prices these same two candidates as an
EXACT bit-for-bit float64 tie** (-0003b's Finding 1: both `108806.04332442369`
at that stage — note this doc's numbers are from a different, non-narrowing
level slice than -0003a/-0003b's `80099.64`-scale L6 figures; both prior
recons independently confirmed the same "exact tie" shape at their own
measurement points). **The near/exact tie is a property of goopg's cost
model alone. It is not present in PG's.** -0003b's Verdict speculated that
PG's own `join_search_one_level` must visit PG's chain first to explain why
PG picks it under an assumed-shared tie — that speculation is now falsified
directly: PG does not need to win a tie, because for PG there is no tie to
win.

## Finding 3 — Q9 and Q45 are NOT "two data points on the same question" (correcting the working-set note)

M0142-0015's forced-PG experiment on TPC-DS Q45 found real PG's default and
forced-alternative orders land within 2 decimal places of each other
(`9827.36` both ways) — a genuine near-tie **reproduced in PG itself**, making
Q45 legitimately a tie-break/enumeration-order-class question. Q9 is
different in kind: goopg's model ties exactly; PG's model does not tie at all
(a 64% gap). Filing them as corroborating witnesses of the same phenomenon (as
this task's own fix_plan line asked, "treat Q9 and Q45 as two data points on
the same question") was premature — only Q45 belongs to the tie-break class.
Q9 belongs to a different, more consequential class: **a real costing-term
divergence between goopg's and PG's cost model, specifically around how much
cheaper an index-driven Nested Loop against a highly-selective
already-shrunk composite (72728 rows) is than a hash join requiring a full
base-table scan (6M rows) of the same relation.** goopg's own trace data is
consistent with this: -0003a's Finding 2 already showed goopg's
`nestloop.index` candidate on PG's own partition prices only ~3–9 cost units
above its `join.hash` winner (`80102.56`/`80108.48` vs `80099.64`) — a tiny
premium goopg's model assigns to the index route in a regime where PG's own
model assigns the SAME route a decisive ~130000-unit *discount* relative to
the hash-with-full-scan alternative. goopg is undervaluing the win (or
overvaluing the hash alternative, or both) for this exact shape.

## Verdict

**M0142-0003c's own premise — that Q9's divergence is an enumeration-order /
tie-break question — is refuted for Q9.** No DP-search reordering, however
carefully scoped, can fix Q9: matching PG's iteration order exactly (which
goopg's structure already does, per Finding 1) changes nothing about which
candidate is cheaper, because the actual defect is that goopg's cost model
computes a near-identical total for two candidates PG's own model separates
by 64%. This reopens the costing-term question -0003a/-0003b's Verdict had
closed ("not a costing-term question... B8/B10 are not implicated by this
specific tie") — B8/B10 are not necessarily the specific terms (that is
un-scoped by this recon), but SOME index-probe-vs-hash-join costing term is
now implicated again, this time with a concrete, measured, 64%-scale PG-side
signal to converge toward rather than a two-decimal-precision goopg-internal
tie to chase. Q45 (M0142-0015) is unaffected by this reversal — it remains a
genuine cross-engine near-tie and stays filed as its own tie-break-class
question.

## Addendum — a prior (pre-M0138) round already named a concrete mechanism for this exact gap

A background research agent dispatched early in this task (before the direct
PG-side experiment above superseded its own approach) surfaced a corroborating
prior investigation this recon had not started from:
`docs/design/not_ralph/plan_parity_fix_take2/r53-q9-costing-step0/{REPORT,SLICE1}.md`
(2026-09-10, **pre-M0138**). Two points corroborate and sharpen this task's
own findings:

- **Enumeration completeness, independently confirmed from goopg's own trace
  (not PG's)**: R53 Step-0 found PG's own L6 partition
  (`{orders}⋈{lineitem,nation,part,partsupp,supplier}`) among 8 partitions ×
  2 orientations offered at L6 with **zero declines** — "the PG shape reaches
  pricing — it is not an enumeration hole." This is Finding 1 confirmed from a
  second, independent angle (goopg's own DPPATH log, rather than this task's
  code-reading of `joinsearchlevel.go`'s structure).
- **A concrete, previously-attributed root cause for the SAME shape of gap**:
  R53 Slice-1 decomposed a (then) 2.5% margin between the same two partitions
  into exact arm terms and found it is **all spill-page cost**: at 64MB
  `work_mem`, PG's own real costing charges its winning partition's build
  side (`{lineitem,nation,part,partsupp,supplier}`, PG's actual byte widths
  14/55/81) **zero spill** (97740 rows × ~60B ≈ 6MB, fits), while goopg's
  `hashJoinCost` (`cost_funcs.go:630`) prices the analogous build using
  `hashsize.EntryBytes`'s **48-B/datum, column-count-based** footprint model
  (not PG's per-column byte-width model), which for the same rowcount charges
  ~743MB — 11.6× over the same 64MB budget — forcing a spill PG's own plan
  never pays. "The footprint model..., not the row estimates alone, is what
  puts the rival over the spill line."

**This does not mean M0142-0003d's mechanism is already found and verified —
it means it is already NAMED, from before this task started, and needs
re-verification rather than re-discovery.** R53 predates M0138 (the ANALYZE
statistics rework) and roughly ten M0142 cost-formula slices (0006-0016) that
landed since; goopg's own internal L6 margin moved from R53's 2.5% down to
0003a/0003b's exact float64 tie across that interval, while this task's own
measurement shows real PG's margin for the analogous whole-query comparison
staying large (64%) throughout. A cost-model term converging toward an exact
tie in goopg's own self-comparison while the PG-relative gap stays large is
itself a signal that whatever changed between R53 and 0003a shifted goopg's
number by coincidence (consistent with -0003a Finding 1's still-unresolved
"coincidence or algebraic identity" question), not by fixing the underlying
spill-footprint mechanism R53 named.

One documentation note surfaced by the same research pass, unrelated to this
task's own scope: `docs/design/leftdeep-joins/03-join-search-pg-dp.md:827-833`
(§8) describes a richer tie-break rule (lexicographic relid-set, then
method-rank) that does not match the tie-break rule actually shipped in
`internal/optimizer/path.go`'s `addToPathlist`/`comparePaths` (first-registered-
wins on an exact/fuzzy tie, `path.go:927-928,1081-1099` — the same rule this
task's own reading and -0003b both already confirmed against PG's
`compare_path_costs_fuzzily`). Not chased here — either a stale/aspirational
design note or an undiscovered second code path; worth a doc-fix task on its
own, out of scope for M0142.

## Deferral

One ledger row: **M0142-0003d**, filed to re-verify (not re-derive from
scratch) whether `hashsize.EntryBytes`'s 48-B/datum column-count-based
footprint model — the mechanism R53 Slice-1 already named for a
same-shape-but-smaller pre-M0138 gap — is still the live cause of this
recon's much larger (64%, whole-query, real-PG-measured) gap, or whether the
~10 cost-formula slices landed since R53 (M0138, M0142-0006..0016) changed
which term dominates. Resume point: instrument goopg's own cost breakdown
(`internal/optimizer/cost_funcs.go`'s `hashJoinCost`, `hashsize.go`'s
`EntryBytes`) for the `join.hash`-on-lineitem-as-base candidate vs the
`nestloop.index`-via-`lineitem_part_supp_fkidx`-analogous candidate at HEAD
(does goopg even generate a comparable "join lineitem to the four-way
dimension composite via an index probe keyed on (partkey,suppkey)" candidate
at the right level — confirm before assuming R53's build-side identification
still applies verbatim) before touching any formula — same "verify both
candidates were generated" discipline as
`planner_verify_both_candidates_generated`.

## Gates

Measurement-only task; `git status --porcelain -- internal/` empty for this
loop (confirmed before writing this doc). No production code touched, so no
values/plan-gate rerun required. The two PG-side `EXPLAIN`s ran against the
shared `:65432` reference cluster read-only (no write, no restart); the
goopg-side evidence was read from already-committed -0003a/-0003b artefacts,
so no goopg server was started this task. Pre-commit hook's pgbench smoke
still runs at commit time per the mandatory-on-every-commit policy.
