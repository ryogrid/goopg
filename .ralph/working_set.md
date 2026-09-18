Task: P0-E7 — bulk re-measurement since 2026-09-16 05:44 (banner item 0,
now selectable: P0-E4/E5/E6 all `[x]` as of 2026-09-18). PARTIAL this loop
(task stays unchecked in fix_plan.md). Committed: 1cc61bb54.

Files: `.ralph/fix_plan.md` (P0-E7 progress note), `.ralph/deferral_ledger.md`
(new row, task-id P0-E7), `docs/design/README.md` (new p0-e7 index row),
`docs/design/0100-0149/p0-e7-bulk-re-measurement.md` (new — full writeup +
the R1-safety correction to m0137-0003's baseline-capture doc),
`analysis/m0137/p0e7-{goopg,pg}-{serial,parallel}.{txt,plans.txt}` (new
capture artifacts). No production code touched.

Key symbols/tools: `cmd/estimate-audit/main.go` (`session.ensure` runs bare
`ANALYZE <table>` on EVERY connection incl. the `-ref-port` one — this is
why the old m0137-0003 direct-`:65432` recipe is now R1-unsafe),
`scripts/lib/tpch-private-clone.sh` (`tpch_private_clone_snapshot`),
`scripts/pg-plan-parity-diff.py`, `scripts/tpch-spotcheck.sh`,
`scripts/tpcds-sf025-regression.sh`.

Findings: values gates clean at HEAD (42a2002ff, same as owner's P0-E6
validation commit 080323cbd — confirmed zero internal/cmd/go.mod/go.sum
diff between them, so this loop's binary is behaviourally identical to the
one P0-E6 already validated). `tpch-spotcheck` PASS Q12=2/Q13=34.
`tpcds-sf025-regression sweep` PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0
TIMEOUT=0, plan-shapes 99/99 identical to prior sweep (e5046bc31). TPC-H
plan-parity (estimate-audit -plan-only, private lane both sides — goopg via
a private 55xx clone, PG via a SECOND invocation pointed straight at
:65432 with -warm-stats=false so no ANALYZE ever runs against it) holds
serial match=8/22, exactly the current floor and unchanged from 27d4ae001
— **no regression found** across the 35 production commits landed since.
Parallel arm match=3/22, recorded but not floor-comparable (tool's own
documented caveat, still valid).

Next step: pick up P0-E7's still-open sub-scope, in whichever order the
banner still finds selectable:
  (a) TPC-DS full-SF1 parity vs `:65438` (`match >= 2` floor) via
      `scripts/capture-tpcds.sh` + `pg-plan-parity-diff.py` on a private
      55xx clone of the SF1 data — budget ~4-5h per the sf025 script's own
      header, so dedicate a whole loop's timeout to it rather than
      starting it with little budget left.
  (b) `scripts/tpch-acceptance-arm.sh` OFF/ON digest comparison + the
      M0142-0008 chain A/B (`admitSemiAnti` on vs off) — this is also
      csq-R2's reopen-condition evidence the owner is waiting on.
  (c) Name each of the 35 production commits between 27d4ae001 and HEAD
      with its own individual TPC-H values result (P0-E7's text asks for
      per-commit naming, not just an aggregate before/after) — likely the
      cheapest remaining piece, do this first if budget is short.
  Re-read the banner first: if a genuine regression or higher-priority
  item appears, follow the banner, not this list.

Gates run: `go build ./...` clean (no production file touched).
`tpch-spotcheck.sh` PASS. `tpcds-sf025-regression.sh sweep` PASS.
`pg-plan-parity-diff.py` (report-only) on both TPC-H arms. `make
ralph-state-guard` clean. Pre-commit hook's pgbench smoke: PASS (ran
automatically on the commit above).

In-flight: none. All private-lane servers/cgroup scopes for this loop's
captures were stopped and reaped before commit (verified via `ps aux` +
`systemctl --user list-units` — clean). Scratch driver
`tmp/p0e7-tpch-parity-capture.sh` and its binary/data dir were removed
after use (not committed — `tmp/` is gitignored); its logic is fully
documented in the design doc's "R1-safety correction" section if it needs
to be reconstructed for the TPC-DS half.
