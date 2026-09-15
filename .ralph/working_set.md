Task: M0142-0012-verify — run the plan-parity floor-measurement suite
M0142-0012 (the decomposed-NLI lateral-index-join cardinality fix) deferred.
**DONE and committed** this loop. One follow-up filed and NOT yet started:
M0142-0014 (triage the 17 TPC-DS plan-shape changes this task's own self-diff
found).

Files: `analysis/m0142/m0142-0012verify-*` (8 new committed artefacts: TPC-H
serial/parallel raw+plans+pg.plans, TPC-DS goopg+pg captures — all against
live `:65432/:65433/:65437/:65438`), `docs/design/0100-0149/
m0142-0012-verify-plan-parity-floor-remeasure.md` (new, full writeup incl.
the "self-diff via pg-plan-parity-diff.py fed an old-goopg capture as its
`pg` arg" technique for a cost-blind shape-delta), `docs/design/README.md`
(indexed), `.ralph/fix_plan.md` (0012-verify `[x]`, 0014 filed `[ ]`),
`.ralph/deferral_ledger.md` (new row).

Key symbols: none touched — pure recon, zero production Go diff (`go build
./...` unaffected). The technique worth remembering for the next loop:
`python3 scripts/pg-plan-parity-diff.py <new-goopg> <old-goopg>` reuses the
tool's own cost-blind shape/category extraction to diff goopg against
itself over time — far more honest than a raw text diff, which a
cardinality-only change contaminates (every EXPLAIN line's cost/row number
changes even when the join shape doesn't; a raw byte-diff of this task's
TPC-DS captures falsely read 97/99 queries as "changed").

Findings this loop: M0142-0012 did **not** move the milestone group's
headline metric on either corpus. TPC-H `-serial` match=6/22 (unchanged),
TPC-H parallel match=2/22 (categories byte-identical to M0137-0017's own
numbers), TPC-DS SF0.25 match=2/99 with the Q9/Q41 floor intact. The
`PLAN-PARITY` aggregate line and 8/9 TPC-DS category counts are unchanged
pre/post M0142-0012 (only `join-order` moves, 90->91). The self-diff shows
this is not "nothing happened": TPC-DS genuinely moved 17/99 plan shapes
(Q4/Q6/Q11/Q25/Q29/Q31/Q34/Q45/Q54/Q56/Q60/Q64/Q72/Q73/Q78/Q79/Q88) — far
fewer than M0142-0012a's 69/99 call-site-hit count (most hits were on
discarded DP candidates or changed a cost without changing the winner) —
but none became a new match and neither existing match was lost. TPC-H
moved **zero** shapes despite 6/21 queries' costs changing. Net effect:
M0142-0012 is a real, verified-correct fix (per its own gates) that
"moved plans sideways" at the milestone-group's own metric — a legitimate,
reportable outcome per `AGENT.md`'s own report contract, not a failure to
diagnose further this loop.

Next step: **M0142-0014** (classify each of the 17 TPC-DS sideways moves as
a neutral trade vs a masked regression, using the already-captured
per-query `SHAPE-DIFF [...]` tags in `analysis/m0142/m0142-0001-tpcds-goopg
.txt` vs `m0142-0012verify-tpcds-goopg.txt` — no re-capture needed) is the
natural next pick, but re-read `.ralph/fix_plan.md`'s `## Current Priority`
banner first per the precedence rule — the banner may have reordered since
this loop started. M0142-0013 (5 NEW ea-ratchet findings from M0142-0012,
still unfiled-for-work) remains an equally-sized alternate pick.

Gates run: `go build ./...` clean (no source touched); all four
`pg-plan-parity-diff.py` invocations report `unparsed=0`; stats-epoch
fingerprints confirmed identical within each corpus's pre/post pair
(TPC-H `253df6b1d97f9f5b`, TPC-DS `5d4dc56356f3d676`) — no ANALYZE drift
contaminated the comparison. Pre-commit pgbench smoke PASS (ran as part of
`git commit`). `make ralph-state-guard`: TBD — run immediately before the
status block per protocol (not yet executed as of this baton write; the
next tool call in this same loop runs it before finishing).

In-flight: none. Started the TPC-DS SF0.25 goopg server
(`bench/tpcds/server.sh start sf025`, it was down at loop start) to run the
captures, then stopped it again via `bench/tpcds/server.sh stop sf025` —
verified `status` shows it down afterward, no stray process left. Pre-existing
unrelated uncommitted changes in the tree at loop start (`.claude/settings.json`,
`.ralph/progress.json`, `analysis/tpch-explain-baseline.md`,
`ci/logs/launch.log`, `ci/logs/scheduler.log`) were left untouched and are
NOT part of this loop's commit (staged this loop's own files by explicit
pathspec only, per the concurrent-Ralph-commit convention).
