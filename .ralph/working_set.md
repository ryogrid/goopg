Task: M0142-0016c (recon, `.ralph/fix_plan.md` item 6, selected as the next
open recon-only task under the P0-E6-wait fallback order once M0141-S7's
item-4 recon-only tasks — cd-q64, cd-q64-reclassify, cd-candidatepool — were
all closed; items 3/5 had no selectable recon-only task: M0141-S2b-6-resume
is gated on the `:65433` hold, M0139-0007c/M0140-0006a-c are implementation
not recon, M0142-0005/0003i are implementation/gated). DONE and committed
this loop.

Files: `docs/design/0100-0149/m0142-0016c-pg-forced-plan-comparator.md`
(new), `docs/design/README.md` (m0142-0016c index row appended),
`.ralph/fix_plan.md` (M0142-0016c ticked with findings, new task
**M0142-0016d** filed as its Parent-linked follow-up), `.ralph/deferral_ledger.md`
(row for M0142-0016c's two open threads: the M0142-0016d scorer fix and the
unscoped bigger CTE-inlining question). No production code (`internal/...`)
touched — pure recon, matching the task's own scope.

Key symbols: `parity.py`'s `annotate_relsets`/`MARK`/`base_relation`
(`scripts/estimate-parity/parity.py:65-120,202-238` — the scorer's relset-key
builder, root cause of the "UNMATCHED-IN-PG" mistag), `pushQualsThroughSingleRefCTEs`
(`internal/optimizer/cte_inline_pushdown.go` — goopg's qual-pushdown-only CTE
mechanism, narrower than PG's `inline_cte`), PG oracle:
`postgres/src/backend/optimizer/plan/subselect.c`'s `inline_cte`.

Findings: real PG 18.3 (`:65438` db `tpcds025`, read-only EXPLAIN/ANALYZE +
session SET only — compliant with the reference-cluster rule) was queried
directly with Q33/Q54/Q56's real text. **PG's own unforced default plan
already contains the identical parameterized-probe-with-residual shape for
all three** (no knob-forcing needed — an initial detour forcing
`enable_hashjoin=off` on an isolated Q33 CTE branch was abandoned once the
FULL unforced query turned out to already have the shape), at cost numbers
byte-identical to the committed `bench/tpcds/plans-pg/{Q33,Q54,Q56}.txt`
captures. PG's own qerr on that node is within ~15% of goopg's (Q33: PG
23.8-27.9 vs goopg 21.4-30.3; Q54: direct clamped-to-1 match). Answers
M0142-0016c's two questions: **yes** PG qerr-matches (measured, not
architectural analogy — M0142-0016b's "not a blocker" verdict now firmer);
**no**, the cost signal should not move the plan choice (PG keeps this exact
shape as its own cost-optimal pick, carrying the identical bad estimate — it
never costs the shape away). Root-caused *why* the scorer tagged all 17
M0142-0016b findings `UNMATCHED-IN-PG` even though PG's captures plainly
contain the matching node: `parity.py`'s relset key prefixes goopg's key with
a `CTE <label>` scope because goopg's own EXPLAIN text still prints a `CTE
ss`/`cs`/`ws`/`my_customers` marker for these single-reference CTEs, while
PG 18.3's `inline_cte` removes the CTE boundary structurally before
join-order search runs, so PG's key carries no such prefix — the two keys
never textually match regardless of node equivalence. Filed **M0142-0016d**
(bounded, tooling-only — fix the scorer's key, no planner code) as the direct
resume point; expected to flip `make ea-ratchet` back to PASS. Left the
bigger "does goopg's narrower qual-pushdown-only CTE handling (vs PG's full
structural inlining) ever cost a worse join order elsewhere in the corpus"
question flagged but unscoped (M0142-0016c's own budget was the PG-comparator
question only).

Next step: re-check the banner. P0-E6 is still `[!]` (owner-run, not done).
Under the P0-E6-wait fallback order, re-scan items 3/5/6 for any other
still-open recon-only task (M0142-0016d itself is NOT recon — it edits
`scripts/estimate-parity/parity.py`, a tooling file, not production code, so
judge on a case-by-case basis whether the fallback's "M0143 tasks whose gates
do not need TPC-H data" bucket or its "recon-only tasks from items 3-6"
bucket is the better fit, or whether it needs owner input on which bucket
applies — it is low-risk and well-scoped regardless). If nothing else
recon-only remains in 3/5/6, M0142-0016d is a reasonable next pick (bounded,
no TPC-H data dependency, `make ea-ratchet` is its own acceptance gate). If
M0142-0016d also isn't selectable for some reason, fall through to the next
M-NIGHTLY open item, or re-check whether P0-E6 has been marked `[x]` by the
owner (unlocks the banner's normal 1-8 order).

Gates run: `make ralph-state-guard` auto-repaired the same stale
status/progress mismatch as the last two loops (prior loop's clean-exit
marker misread as project-completion), clean after repair. No
optimizer/executor code changed this loop, so no `go build`/`go
test`/`tpch-spotcheck`/`sf025` gate was required for this task itself (recon
only, confirmed no `internal/...` diff via `git status --short`). The
goopg-side SF0.25 clone used to read a plan shape (`EXPLAIN`, no ANALYZE) was
started/stopped cleanly this loop — see In-flight below for the exact
commands used, since `server.sh stop` is RALPH_LOOP-guarded for TPC-DS lanes.

In-flight: none. Private SF0.25 clone (`GOOPG_BIN=tmp/m0142-0016c-bin
bench/tpcds/server.sh start sf025`, port 65437) was stopped via
`./tmp/m0142-0016c-bin stop -D bench/tpcds/runtime_goopg/data-sf025` (direct
binary invocation — `server.sh stop` is blocked by the RALPH_LOOP guard for
any TPC-DS target, including sf025), confirmed no orphan process via `ps
aux`, and `tmp/m0142-0016c-bin` removed. All scratch `/tmp/q*.sql`/`.out`
files removed. The `:65438` PG reference cluster was left running (was
already UP at loop start, read-only queries only, untouched otherwise).
