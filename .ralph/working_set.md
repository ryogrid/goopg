Task: P0-E7 — bulk re-measurement since 2026-09-16 05:44 (banner item 0,
still the only selectable item). PARTIAL this loop (task stays unchecked
in fix_plan.md). Committed: c6d5747f8.

Files: `docs/design/0100-0149/p0-e7-bulk-re-measurement.md` (new
"tpch-acceptance-arm.sh OFF/ON digest + M0142-0008 chain A/B" section,
Status line update, "Not yet measured" note), `.ralph/fix_plan.md`
(P0-E7 progress note, third sub-entry, 2026-09-18c), `.ralph/deferral_ledger.md`
(new row, task-id P0-E7, closes the acceptance-arm/M0142-0008 items),
`docs/design/README.md` (p0-e7 index row updated). Two throwaway
binaries built+deleted (`tmp/goopg-p0e7-abtest-{on,off}` +
`-runner`, tmp/ is gitignored). New tracked artifacts:
`analysis/m0142/p0e7-admitsemianti-{off,on}.txt` +
`.diff-vs-baseline.txt`. No production code touched (the OFF-arm
patch to `internal/optimizer/joinsearchseam.go:313` was local, built,
then `git checkout`'d back to HEAD before either arm ran — verified
clean before commit).

Key symbols/tools: `scripts/tpch-acceptance-arm.sh` (NO_BUILD=1,
pre-built pinned binaries via GOOPG_BIN/RUNNER_BIN), the ONE production
call site `extractSearchLeaves(chain, true)` at
`internal/optimizer/joinsearchseam.go:313` (the M0142-0008 chain's only
live effect).

Findings: ON (HEAD, admitSemiAnti=true) vs OFF (local-patch,
admitSemiAnti=false) TPC-H SF1 22-query `-digest` sweep: **23/24 digest
lines byte-identical**. Sole divergence Q9 — times out at the 600s cap
in BOTH arms, differing only in which of two simultaneous cancellation
paths won ("statement timeout" server-side vs "user request" runner
client-side, ~0.1s apart) — a scheduling race on an already-known-slow
query (`q9_costdriven_mhj_cannot_be_cost_forced` memory), not a value
or plan effect of the flag. Runner's own `-diff` reads VERDICT: FAIL
only because it treats the two different error strings as a mismatch;
this is documented in the design doc as the expected, understood
outcome, not a blocking regression. This is the A/B evidence P0-E7 /
csq-R2's reopen condition needed for the owner's freeze decision on the
M0142-0008 chain (still FROZEN either way per the 2026-09-17 owner
note — this loop only supplies the evidence, not the decision).

Next step: P0-E7's ONE remaining sub-item is TPC-DS full-SF1 parity vs
`:65438` (`match >= 2` floor) via `scripts/capture-tpcds.sh` +
`pg-plan-parity-diff.py` on a private 55xx clone of the SF1 data —
budget ~4-5h per the sf025 script's own header, dedicate a whole loop's
timeout to it. Once it lands, P0-E7 can be ticked `[x]` in fix_plan.md
and banner item 1 (regressions found by P0-E7, if any) becomes
selectable — re-read the banner first in case it changed. Also: once
the owner records the M0142-0008 freeze decision in the banner, P0-H11
(stale-comment cleanup, currently blocked) becomes selectable.

Gates run: `go build ./...` clean (before/after the local patch and
after the revert). `python3 scripts/ralph_protected_regions.py
check-designdocs` exit 0. Pre-commit hook's pgbench smoke: PASS (ran
automatically on the commit above). `tpch-acceptance-arm.sh` ran twice
live (the actual measurement, not a gate in the pass/fail sense —
stamped FAIL for the documented Q9-race reason, see design doc).
`make ralph-state-guard`: found status/progress inconsistency from the
prior loop's clean-exit "completed" marker, self-repaired to
"in_progress", clean on re-check (same self-repairing pattern as the
prior loop).

In-flight: none. No servers/scopes left running (verified via pgrep
after both arms completed; `systemctl --user list-units
'goopg-tpch-acceptance-*'` empty). Both throwaway abtest binaries
deleted.
