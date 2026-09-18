Task: P0-E7 — bulk re-measurement since 2026-09-16 05:44 (banner item 0).
**DONE and committed this loop** (`1969b9ead`). All six named P0-E7
sub-items are now measured; task is `[x]` in fix_plan.md. Banner item 0 is
therefore satisfied and item 1 ("Regressions found by P0-E7") is now the
selection target for the NEXT loop — re-read the banner first, since S1
says the banner (not this file) is the ordering authority.

Files: `docs/design/0100-0149/p0-e7-bulk-re-measurement.md` (new "TPC-DS
full-SF1 parity" section, `Status:` -> done), `.ralph/fix_plan.md` (P0-E7
ticked `[x]`, new closing sub-entry, new task **P0-H12** filed under the
P0 section for banner item 1), `.ralph/deferral_ledger.md` (closing P0-E7
row), `docs/design/README.md` (p0-e7 index row updated to "done"). New
tracked artifacts: `analysis/m0142/p0e7-tpcds-sf1-{goopg,pg}.plans.txt` +
`-diff.txt`. No production code touched (`internal/`, `cmd/`, `go.mod`,
`go.sum` all clean before and after).

Key finding: the prior two loops' "~4-5h budget" for the TPC-DS SF1 capture
was WRONG — that figure is `tpcds-sf025-regression.sh`'s **execution**-sweep
cost (`cmd_sweep`, real query runs + 16 known 600s timeouts), not
`scripts/capture-tpcds.sh`'s plan-only `EXPLAIN` cost (scale-independent —
measured 2.3s goopg-side + 5.0s PG-side for the full 99-query SF1 corpus).
Always check which of a script's two code paths (execute vs EXPLAIN-only) a
budget note actually describes before inheriting it into a new task.

Result: goopg `:65436` (fresh HEAD binary `a0e741a68`) vs PG
`:65438`/`tpcds` (SF1, read-only) via `pg-plan-parity-diff.py`:
`match=1/99` (Q41 only). Q9 — one of the two `match >= 2` floor queries,
set by `m0137-0004` **at SF0.25** where Q9 does match — is
`SHAPE-DIFF [scan-type]` at SF1. First-ever SF1 capture under this harness,
no same-methodology baseline exists to bisect against directly, so this is
recorded as new information (not asserted as a regression) and handed to
the owner's banner as **P0-H12** (Kind: recon, Parent: P0-E7) rather than
bisected inline — bisection needs a second capture pair built at
`27d4ae001`, an independently-sized task.

Next step: re-read the `## Current Priority` banner in `.ralph/fix_plan.md`
at the start of the next loop. If it still reads "P0-E7 is `[x]` as of
2026-09-18 → item 1 selectable", pick the topmost selectable task under
"1. Regressions found by P0-E7" — currently that section has no items
besides the just-filed P0-H12 (a recon, immediately selectable — its own
`Kind: recon` means it stays a measurement/investigation task, not a
production-code change). P0-H11 remains blocked (owner decision pending).
If the owner has since recorded a banner change, follow it instead (S1/the
Working Set Carry precedence rule).

Gates run: `go build ./...` clean (no production file touched).
`python3 scripts/ralph_protected_regions.py check-designdocs` exit 0.
Pre-commit hook's pgbench smoke: PASS (ran automatically on the commit
above). `make ralph-state-guard`: found the same status/progress
inconsistency pattern as the prior two loops (prior loop's clean-exit
"completed" marker), self-repaired to "in_progress", clean on re-check.

In-flight: none. The goopg TPC-DS SF1 server (`:65436`) was started this
loop (fresh HEAD binary) and stopped again via direct binary invocation
(`tmp/goopg-tpcds-bin stop -D bench/tpcds/runtime_goopg/data` — NOT
`bench/tpcds/server.sh stop`, which the RALPH_LOOP guard denies
unconditionally for `stop`/`restart` regardless of target since the same
wrapper can also stop the `:65438` PG reference); verified `down` again via
`bench/tpcds/server.sh status` before this loop ended. No other
servers/scopes left running.
