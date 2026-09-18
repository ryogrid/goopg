Task: M0141-S2b-6-resume — repeat S2b-6's Hashed-vs-Sorted `PathAgg`
term-by-term cost diff against the real HammerDB SF1-loaded `:65433`
cluster (gate cleared by P0-E6/P0-E7). DONE and committed this loop.
**NEXT LOOP should re-read the banner first** (S1 precedence) — banner
item 3's own ordering is: fix1-sweep's filed children (done),
M0141-S2b-6-resume (done this loop), then **M0139-0007c**. Select
M0139-0007c next unless the banner has changed. A new follow-up,
**M0141-S2b-10** (`Kind: recon`, root-cause `costAgg`'s Hashed-vs-Sorted
formula gap for TPC-H Q4/Q12), was also filed under the same M0141-S2b
lineage but is NOT itself the banner's next pick — it only becomes
selectable once/if the banner's own ordering reaches it.

Files: `internal/optimizer/pathtrace.go` (new
`traceOrderedGroupingCandidate`/`traceOrderedSortedCandidate`, both
`pathTraceEnabled`-gated). `internal/optimizer/upperordered.go`
(`addOrderedPaths` calls both, gated `input.Kind == PathAgg`). Design doc:
`docs/design/0100-0149/m0141-s2b-6-resume-hashed-vs-sorted-real-sf1.md`
(new — the parent `m0141-s2b-scoping-decomposition.md` is frozen at 977
lines under D3.1, so this is a fresh file, NOT an appended section;
do not append to the parent doc without splitting it first).
`docs/design/README.md` (new index row). `.ralph/fix_plan.md`
(M0141-S2b-6-resume ticked `[x]`; new task M0141-S2b-10 filed).

Key symbols: `electOrderedGrouping`/`addOrderedPaths`
(`upperorderedgrouping.go`/`upperordered.go`) — the Hashed-vs-Sorted
`PathAgg` election site. `traceOrderedGroupingCandidate`/
`traceOrderedSortedCandidate` (new, `pathtrace.go`) — DPPATH lines keyed
by `AggStrategy` instead of inferred from `contained`. `costAgg`
(`cost_funcs.go`) — NOT yet term-by-term diffed against PG's `cost_agg`;
that is M0141-S2b-10's job.

Finding: S2b-6's synthetic-dataset tie does NOT survive real SF1 data —
all four of Q4/Q5/Q12/Q21 are real, non-tied margins. Q5/Q21 reconfirm
"no cost bug" (their ORDER BY never matches GROUP BY). Q4/Q12 are a
**confirmed real cost-model divergence from PG**: goopg elects
Hashed+Sort (899.76/2181.96-unit margins), a **fresh** live `EXPLAIN` on
the read-only `:65432` PG reference elects the mirror-image Sorted shape
for both. Tested and ruled out the R113 `GOOPG_PG_SORT_RELATION_BYTES_COST`
Sort-byte-size-currency GUC as the cause (env-toggle-only experiment,
`currency=pg` confirmed active, election and margin both unchanged) — the
real cause is unconfirmed, most likely `costAgg`'s `AggStrategyHashed`
formula, filed as M0141-S2b-10 for a future loop.

Naming trap hit and fixed this loop: filed the follow-up as
`M0141-S2b-8` initially without checking for collisions — `M0141-S2b-8`
and `-9` were ALREADY taken by an unrelated TPC-DS Incremental-Sort
candidate-pool line of work (`M0141-S7-cd-q64-reclassify`/
`M0141-S7-cd-candidatepool`, both landed 2026-09-18 same day). Caught via
`grep -oE "M0141-S2b-[0-9a-z]+" .ralph/fix_plan.md | sort -u` before
committing; renamed to `M0141-S2b-10` in all three touched files
(fix_plan.md, README.md, the new design doc). **Always run that grep
before naming a new sub-task under a deeply-forked lineage id** — two
sibling investigations under the same parent milestone can independently
reach for the "next" number on the same day.

Gates run: `go build ./...` clean. `go test ./internal/optimizer/...`
PASS (full package, no `-count=1`). `scripts/tpch-estimate-audit-arm.sh`
x2 (baseline PGSHAPED=1 arm + GOOPG_PG_SORT_RELATION_BYTES_COST=1 arm),
both rc=0, served-binary sha256 verified, private port 5582, online
`pg_basebackup -X fetch` clone off live `:65433` (never stopped).
`scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=34), staged-tree gate stamp
PASS. `scripts/tpcds-sf025-regression.sh sweep` PASS=96 MISMATCH=0
CKMISMATCH=0 ERROR=0 TIMEOUT=0, `PLAN-SHAPE: queries=99 same=99
changed=0` (confirms zero plan movement — trace-only change), staged-tree
gate stamp PASS. `python3 scripts/ralph_protected_regions.py
check-designdocs` exit 0. `python3 scripts/ralph-lineage-guard.py` clean
(after the Movement:none fix and the S2b-10 rename above).
`make ralph-state-guard`: found status="running"/progress="completed"
inconsistency (prior loop's clean-exit marker), auto-repaired to
`in_progress`, then clean.

In-flight: none. Both private-lane arm servers (port 5582) stopped by
the script's own EXIT trap; verified via `pgrep -af "goopg.*5582"` (no
match). Scratch output files (`analysis/leftdeep-joins/m0141-s2b6-resume-
2026-09-18{,b}.{txt,plans.txt}`) deleted after their content was folded
into the design doc — same precedent as S2b-5/S2b-6's own scratch
probes. `tmp/goopg-audit-arm-tpch-data` (the private clone, ~2GB) left in
place under `tmp/` (gitignored) for reuse by a future arm run, matching
the private-clone lane's own reuse convention. Verified only the
legitimate shared `goopg-ref-tpch.scope` (`:65433`) remains running;
`lineitem` row count re-checked unchanged (6001255) after both arms.
No shared cluster (`:65432`/`:65433`/`:65437`/`:65438`) was started,
stopped, reset, or written beyond `pg_basebackup -X fetch` (source, never
its destination) and read-only `SELECT`/`EXPLAIN`.
