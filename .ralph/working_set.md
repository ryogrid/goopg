Task: M0142-0004 — re-measure TPC-DS's row-estimate error at HEAD, per
banner item 4. **DONE this loop, measurement-only** (matching the
M0142-0003a/0003b recon-task precedent): analyzed the `make ea-ratchet`
artifacts M0137-0018 (previous loop) had already captured at this same HEAD
(no planner/executor/catalog diff between that commit and this one), rather
than re-running the ~10min capture for an unchanged code state.

Files: `.ralph/fix_plan.md` (M0142-0004 marked `[x]`; two new `[ ]`
sub-tasks filed: M0142-0004a/0004b), `.ralph/deferral_ledger.md` (new row
`m0142-0004`, status `-`), `docs/design/0100-0149/m0142-0004-tpcds-rowest-remeasure-at-head.md`
(new), `docs/design/README.md` (+1 row). Also refreshed the stale memory
topic file `goopg_tpcds_rowest_off_by_orders.md` (its 2026-09-06 examples
are all fixed at HEAD) and shrank `MEMORY.md` from 28.6KB to 17.1KB (it had
silently exceeded its 24.4KB read limit — entries past line 127 were
already being dropped on load; trimmed every line and removed ~10 fully
narrow/stale entries). No production Go code touched this loop.

Key symbols/tools: `analysis/planner-refactor-take3/c20a-estimator-census-20260915/ea-findings-20260915.json`
(605 nodes scored, 140 flagged `qerr>=10` — the artifact this task's finding
is built from); `internal/optimizer/cardinality.go:329` `indexScanRows`
(the unique-pkey `est=1` special case, C1's subject); `:208` `estimateSetOp`
(C2's subject — itself PG-faithful, so the bug is upstream of it);
`internal/optimizer/joinkeyproof.go` `resolveBaseColumn` family (B2's arm
list, confirmed still missing `*Append`/`*SetOp`, but latent — no live
witness).

Findings: **distribution** 20 findings >=1000x, 41 at 100x-1000x, 79 at
10x-100x, max 14,500x (Q47) — read as "~1-4 orders out for ~23% of scored
nodes", NOT a repeat of the old single-anecdote "3-5 orders" (that anecdote,
Q22, now has zero findings). **B2 reclassified**: structurally still absent
in code, but its original witness (Q76) no longer shows any qerr>=10
finding — latent, not closed, no active symptom to chase right now.
**Two NEW mechanisms filed, neither previously in the ledger**: (C1) 19
findings are `Index Scan using X_pkey` nodes at `est=1` where PG's OWN
estimate for the same named index is ALSO far from 1 (q34
`household_demographics_pkey`: PG=489 vs actual=10082) — leading hypothesis
is a loops-vs-total EXPLAIN ANALYZE capture-tooling artifact (repeated
per-outer-row subplan execution), not necessarily a shared planner bug;
UNVERIFIED, next step is reading one witness's raw `loops=` field. (C2) a
3-way CTE `UNION ALL` (Q33/56/60: `cs`/`ss`/`ws`) estimates `est=3` against
actuals up to 1557 — `estimateSetOp`'s arithmetic is correct, so each CTE
branch's OWN row estimate is what collapses to ~1; UNVERIFIED which node in
the branch subtree does it.

In-flight: none. No server/gate process left running.

Next step: per the banner, item 4 is still open (M0142-0004 was only its
first pick). Candidates for the next loop, all reasonable: **M0142-0004a**
(recon: is C1 real or a capture artifact — cheapest, answers a measurement
question about the harness itself before any more `ea-ratchet` output is
trusted), **M0142-0004b** (recon: trace C2's CTE-branch collapse), or
continuing item 4's other named work (M0141 S2b/S3-S7, M0142-0005 onward).
M0142-0004a is the recommended pick — it is cheap (single-witness EXPLAIN
read, no code change expected) and its answer determines whether 19 of the
140 `ea-ratchet` findings are trustworthy going forward, which affects how
every future loop reads that gate's output.

Gates run: `make ralph-state-guard` — one self-repair (the same recurring
benign stale-clean-exit-marker pattern several prior loops have noted),
clean after repair. Pre-commit hook's pgbench smoke will run at commit
time. No `go test`/`tpch-spotcheck.sh`/TPC-DS sweep run this loop — no
production Go code changed (measurement/filing-only, reusing an
already-captured artifact), matching the precedent set by
M0137-0020/M0139-0007/M0142-0003a/0003b's own recon-and-file closures.
