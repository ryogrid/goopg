# R22 — DECLINED: the plain arm is provably unwinnable (dominance proof
+ production probe)

*Round close-out, 2026-09-09. Status: design committed, implementation
WRITTEN, unit-pinned, driver-pinned, production-probed — then REVERTED
in full. Verified out-of-scope verdict (goal permits), not an
unfinished round.*

## 0. Verdict in one paragraph

The unconditional plain-index arm was implemented exactly as designed
and then killed by its own gate test: through the real driver it files
zero paths, and the reason is structural, not a bug — a full-fetch
index scan is strictly dominated by the seq scan on both cost axes
(index I/O ≥ seq I/O at 4× page cost, plus a descent startup the seq
scan prices at zero), so `addToPathlist` drops it whenever a seq path
exists, which is always. A production probe (temporary counter,
22 TPC-H EXPLAINs, reverted) measured **145 offers, 0 survivals**.
Landing it would file candidates into every search that die in the
same comparison — pure CPU and pathlist churn for zero new shapes.
Reverted in full; tree clean.

## 1. What was built and measured

- `addOnePlainIndexPath` (btree-only, predicate-decline, R1 qpqual
  currency, serial + C-19c partial twin) + loop restructure keeping
  the ordered arm byte-identical behind its gate.
- Unit pins (all passing pre-revert): offered-without-gate, shape
  (pathkey-less/clauseless/unparameterised/forward), cost identity
  with the ordered full-scan pricing, declines (predicate/hash-AM),
  partial twin filed.
- Driver pin: gate-declined fixture files 2 plain paths through the
  real `addOrderedIndexPaths` loop.
- Production probe (temporary `R22PROBE` log, reverted): 145 plain
  offers across TPC-H (every rel × every btree index, incl.
  nation_pk/region_pk), **0 survivals**.
- TPC-H A/B pre/post (same clone, verified binaries): **zero byte
  changes, 22/22** — corroboration, not the verdict (the verdict is
  the dominance proof below + the probe).

## 2. The dominance proof (why no corpus could ever differ)

For a full fetch (`selectivity = 1.0`), `costIndexScan` vs `costSeqscan`
on the same rel: index I/O ≥ seq I/O (random 4× page cost vs 1×; even
at correlation 1.0 the correlated bound is seq pages + one random page
+ descent startup vs seq's zero startup), CPU terms identical (same
`numQualOps`), startup strictly greater (descent > 0 = seq 0).
`comparePaths` therefore yields seq-dominates on cost with pathkeys/
ParallelSafe/RequiredOuter all equal — and on an exact tie `add_path`
keeps the incumbent. The plain path survives only where NO seq path
exists, which production never presents (every rel seeds one).
QED without running a single query; the probe confirmed it (145/0).

Corollary that reframes the motivating evidence: PG's own
`Index Scan using nation_pk` sites (Q5 fixture, Q8 store_pkey) are NOT
plain-path wins — PG's add_path prunes the same way. Live PG shows
zero cond-less index scans on TPC-H; the two TPC-DS ones (Q8 store,
Q79 customer) sit where goopg's real gap is the WIDTH model (store:
goopg width 676 vs PG 20 — a different round, not candidacy), and
live-PG Q5 itself seq-scans nation (the fixture evidence was stale;
K9's lesson, applied to my own §0).

## 3. What this means for the TODO row

The row asked to "drop/relax the gate so a plain index path is always
a candidate". Done literally, the candidate dies in addPath by
dominance — the gate was never what kept these plans away. The live
question the evidence actually supports: ORDERED index paths (which
survive via pathkey incomparability) at the Q5/Q8 nation sites, i.e.
the ordered arm's contest, not a plain arm. No new round is filed
from here: that contest is join-order/upper-planner territory (K24,
R21-slice-3), already owned.

## 4. Gates

- Suites: optimizer + executor green (pre-revert; post-revert tree =
  HEAD + R25-slice-1 only, suites re-run there).
- Values: vacuous — nothing landed. The TPC-H digest that died
  mid-Q1 was an infrastructure kill (global OOM, peer's 9.5 GB
  uncapped scope + mine), not a product signal; re-run on the R25
  lane if values evidence is ever needed for THESE commits (it is
  not — docs only).
- Parity: the A/B (22/22 identical) + the probe (145/0) are the
  evidence. Timing: not taken — nothing changed.

## 5. Review record

Subagent delegation unavailable (Task cancelled at R0; TODO.md log).
Review as adversarial second pass by the author:

- **Falsification chain, in order:** (1) A/B zero movement →
  refused the "no effect" reading without distinguishing
  offered-but-loses from never-offered (K4); (2) driver test RED
  (0 filed) → first hypothesis "my loop broke" — falsified by
  reading the fixture (pre-existing seq rival dominates); (3) live-PG
  fixture evidence (Q5/Q8 nation) → falsified by LIVE PG (zero
  cond-less index scans on TPC-H; Q5 itself seq-scans); (4)does PG's own add_path keep plain paths? → answered by the
  dominance proof, which applies equally to PG's comparator —
  consistent with live PG showing none.
- **K22 guard:** this report states its limitation — the proof covers
  full-fetch plain scans under the current cost functions. A future
  cost change (e.g. correlation ≈ 1 making min_IO_cost undercut seq,
  or a descent-charge recalibration) could un-dominate them; the
  design doc is retained so the arm can be rebuilt, not re-derived.
- **Adjacent finding, not fixed (scope):** the prebuilt SEED never
  counts `enable_seqscan` (`newPrebuiltPath` sets no DisabledNodes),
  so B-17d's "producers count the toggle" does not cover the path
  most likely to win. Pre-existing, out of this round.
