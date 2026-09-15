Task: M0137-0018 — re-score `make ea-ratchet` at HEAD, settle its harness
standing. **DONE this loop** (`c89615911`, committed). Banner item 3
(M0138 0007-0009) was already fully `[x]` before this loop. M0137-0018 was
selected instead of M0140-0006 (item 3's other remaining task) because
M0140-0006's own recon sizes it as **three separate C-19-series-slice-sized
sub-parts** (shared SetOp-branch entry point rework, a new partial-Append
cost producer, new executor claim-set logic) — out of reach for one loop —
while M0137-0018 was a bounded, well-scoped measurement/harness task sitting
earlier in the file, matching the working_set precedent from two loops ago
("re-check whether the banner has promoted something smaller").

Files: `AGENT.md` (+new "estimate-accuracy instrument" table under
Plan-parity-harness measurement section), `Makefile` (3 stale `SF0.5`/`~1h`
mentions corrected to `SF0.25`/`~10min`, one also mislabelled port 65437),
`scripts/estimate-parity-gate.sh` (`EA_BASELINE` default repointed at the
new 20260915 baseline dir), `docs/design/0100-0149/
m0137-0018-ea-ratchet-rescore-and-harness-standing.md` (new),
`docs/design/README.md` (+1 row), `.ralph/fix_plan.md` (M0137-0018 `[x]`),
`.ralph/deferral_ledger.md` (new row `m0137-0018-ea-ratchet-stale-baseline`,
resolved), `analysis/planner-refactor-take3/c20a-estimator-census-20260915/`
(new: ea-baseline.txt, ea-capture-20260915.txt(+.header), ea-findings.json).
No production Go code touched (measurement/harness-only task).

Key symbols/tools: `scripts/estimate-parity-gate.sh` (EA_SRC_DATA/EA_BASELINE
env vars, `score()` function); `scripts/estimate-parity/parity.py`
(`--write-baseline` re-pins, `--baseline` ratchets); the re-score ran via
`EA_PORT=5534 EA_CG_UNIT=goopg-ea-ratchet make ea-ratchet` (private clone
`tmp/c20a/data-sf025`, own cgroup, never touches shared `:65437`).

Findings: re-scored at HEAD (commit `4c5c13905`), clean 99/99-query capture,
`FINDINGS: 140` vs the 2026-09-07 pinned baseline's 178 — RATCHET showed 99
FIXED / 61 NEW, `EA-RATCHET: FAIL`. Before trusting that as a real
regression signal, traced why: `git log -p` on
`scripts/estimate-parity-gate.sh` showed commit `e2a50de40` ("SF0.5→SF0.25
dev-gate migration", 2026-09-11 — **4 days after** the baseline was pinned)
moved `EA_SRC_DATA` from the SF0.5 to the SF0.25 goopg cluster **and**
regenerated every `bench/tpcds/plans-pg/Q*.txt` PG fixture in the same
commit, but never re-pinned `ea-baseline.txt`. Every `make ea-ratchet` run
since 2026-09-11 (including this task's first re-score) has therefore been
comparing SF0.25 finding identities against an SF0.5-era pinned set — the
61-new/99-fixed delta is contaminated by an unknown mix of real estimator
drift and pure scale-mismatch artifact, and the two are not separable after
the fact (the SF0.5 corpus was replaced, not archived, by the migration).
Resolved by re-pinning a fresh baseline off the same capture
(`analysis/planner-refactor-take3/c20a-estimator-census-20260915/`, 140
findings) and verifying it scores clean (`EA-RATCHET: PASS`, `baseline
findings: 140 current: 140`) via `EA_CAPTURE=<file> make ea-ratchet`
(no server). Added `make ea-ratchet` to AGENT.md's harness measurement
section (a third table, since it measures estimate quality — a K50 blind
spot neither the plan-parity nor the values-gate tables can see) with the
re-pin-on-corpus-change rule spelled out so this exact trap doesn't recur.
Scoped out (per the design doc): no individual finding in the 61-new/99-fixed
lists was triaged — this task establishes a trustworthy baseline going
forward, it does not investigate any specific estimator's correctness.

In-flight: none. The EA scratch server (port 5534, cgroup goopg-ea-ratchet)
was stopped cleanly by the script's own `trap stop_server EXIT` — verified
`pg_isready -p 5534` refuses after the run. No shared cluster (`:65436`,
`:65437`) touched or restarted this loop.

Next step: re-read the `## Current Priority` banner fresh. Per this loop's
read, banner item 3 (M0138 + M0140) has only **M0140-0006** left, and it is
oversized for one loop per its own recon (3 C-19-slice-sized sub-parts: (1)
expose SetOp branches' `PartialPathlist` instead of a finished `Node` at
`planner.go:1114` — a shared entry point every SetOp query uses, not a
Q5/Q76-scoped edit, (2) a new partial-Append cost producer mirroring
`addPartialHashJoinPath`'s shape, (3) new executor claim-set logic since
`setOp` has no `parallel_scan.go`-style worker-partitioning — wrapping it in
`Gather` today would duplicate every row). Next loop should either: (a)
decompose M0140-0006 into a first bounded sub-slice (e.g. just the
SetOp-branch `PartialPathlist` exposure, with the cost producer and claim-set
logic filed as its own follow-on tasks per the group's two-artefact deferral
rule), or (b) check M0137-0019/M0137-0021 (both filed, unowned,
investigative/re-measure tasks — no longer flagged as "smaller" now that
M0137-0018 is closed, so normal banner order applies: they rank after M0138/
M0140 per the banner's own item-3 listing, not before).

Gates run: `bash -n scripts/estimate-parity-gate.sh` clean. Direct re-pin
verification (`EA_CAPTURE=<file> make ea-ratchet` twice: once against the
old 20260907 baseline showing FAIL 61-new, once against the new 20260915
baseline showing PASS) — both ran clean, no server needed for either.
`make ralph-state-guard`: one self-repair (same recurring benign
stale-clean-exit-marker pattern several prior loops have noted), clean
after repair. Pre-commit hook's pgbench smoke: PASS (tps 43-151, 0 failed).
No `go test`/`tpch-spotcheck.sh`/TPC-DS sweep run this loop — no production
Go code changed, matching M0137-0020's own precedent for harness-only tasks.
