Task: P0-E7 — bulk re-measurement since 2026-09-16 05:44 (banner item 0,
still the only selectable item). PARTIAL this loop (task stays unchecked
in fix_plan.md). Committed: cb6fd5b16.

Files: `docs/design/0100-0149/p0-e7-bulk-re-measurement.md` (new
"Per-commit TPC-H values naming" section + Status line + "Not yet
measured" update), `.ralph/fix_plan.md` (P0-E7 progress note, second
sub-entry), `.ralph/deferral_ledger.md` (new row, task-id P0-E7),
`docs/design/README.md` (p0-e7 index row updated). No production code
touched.

Key symbols/tools: plain `git log`/`git diff-tree --name-only` filtered to
`^(internal/|cmd/|go\.mod|go\.sum)` — no new tooling, just enumeration.

Findings: git-derived the exact production-commit set between `27d4ae001`
and HEAD is **72 commits**, not the banner's approximate "35" (that number
was the owner's own estimate at filing time; used the accurate count
instead of chasing the discrepancy). Bucketed by gate status at landing
per the banner's own boundary commits: pre-incident-fix
(`27d4ae001`..`4f6f81734`] = 49, SKIP-BLOCKED-under-hold
(`4f6f81734`..`c7e231ae1`] = 20 (banner said "18" — approximation, not a
real gap), post-hold/pre-restore (`c7e231ae1`..HEAD] = 3 (includes the 2
M-NIGHTLY "checked by nothing" commits; the 3rd named M-NIGHTLY commit
`c03742e2f` is already in the SKIP-BLOCKED bucket). All 72 inherit the
PASS result from the single HEAD binary measurement the prior loop already
recorded (`tpch-spotcheck` PASS Q12=2/Q13=34, TPC-H plan-parity serial
match=8/22 unchanged floor, tpcds-sf025 sweep PASS=96/96) — no
per-commit rebuild/re-test was needed since no regression was found
end-to-end and bisection is only warranted on a found regression.

Next step: pick up P0-E7's two still-open sub-items, in whichever order
the banner still finds selectable:
  (a) TPC-DS full-SF1 parity vs `:65438` (`match >= 2` floor) via
      `scripts/capture-tpcds.sh` + `pg-plan-parity-diff.py` on a private
      55xx clone of the SF1 data — budget ~4-5h per the sf025 script's own
      header, dedicate a whole loop's timeout to it.
  (b) `scripts/tpch-acceptance-arm.sh` OFF/ON digest comparison + the
      M0142-0008 chain A/B (`admitSemiAnti` on vs off) — this is also
      csq-R2's reopen-condition evidence the owner is waiting on.
  Once both land, P0-E7 can be ticked `[x]` in fix_plan.md and banner
  item 1 (regressions found by P0-E7, if any) becomes selectable —
  re-read the banner first in case it changed.

Gates run: `go build ./...` clean (no production file touched).
`python3 scripts/ralph_protected_regions.py check-designdocs` exit 0.
`make ralph-state-guard` — found status/progress inconsistency from a
prior loop's stale "completed" marker, self-repaired to "in_progress",
clean on re-check. Pre-commit hook's pgbench smoke: PASS (ran
automatically on the commit above). No new TPC-H/TPC-DS gate run this
loop — this loop's contribution was a report-only re-derivation from
already-recorded HEAD gate results, not a new measurement.

In-flight: none. No servers/clones/scopes started this loop (pure git log
+ doc writing, no cluster access).
