Task: M0137-0014 — automate the seam-decline census (banner's item 2, "M0137's
re-opened tasks 0014-0017"). **DONE this loop** (`0779981e1`, committed).
Item 2 is now 3-of-4 done (0014, 0015, 0016). Only **M0137-0017** remains.

Files: `scripts/seam-decline-census.py` (new — parses `traceSeamDecline`'s
`seam-decline reason=<class>` stderr lines, required `--timeout` label,
report-only exit 0), `scripts/seam-decline-census-test.py` (new, mirrors
`qual-placement-census-test.py`'s unittest+subprocess pattern, 7 tests),
`docs/design/0100-0149/m0137-0014-seam-decline-census-tool.md` (new),
`docs/design/README.md` (+1 row), `.ralph/fix_plan.md` (M0137-0014 checked
`[x]`). No ledger row: pure tooling, no PG-incompatibility discovered.

Key symbols: `traceSeamDecline` (internal/optimizer/joinsearchtrace.go:650,
fixed 18-member reason vocabulary across `joinsearchseam.go` and
`relfromjoinlist.go`), the new script's `census()`/`format_report()`
functions (mirrors `qual-placement-census.py`'s module shape per that
file's own established convention for this tool family).

Findings: AGENT.md item 4 ("seam-decline census by class... at a stated
timeout") had zero producer — the 2026-09-15 harness-defect review
(`tmp/METHODLOGY3_RALPH_CHECK0915/03-harness-defects.md`) measured only
3/22 task reports carried it. The manual fallback the harness names is
`grep -oP "seam-decline reason=\K\S+" <log> | sort | uniq -c`; the new
tool automates exactly that and was cross-checked byte-for-byte against a
real committed capture
(`analysis/planner-refactor-take3/c06-q13-diagnosis-20260907/evidence/dppath-on.txt`,
`declines=1 reason=outer-link-no-sjinfo`), not just synthetic fixtures.
`--timeout` is required (not defaulted/parsed) because AGENT.md's own
wording is "censuses are only comparable at equal timeouts" — the tool
must not let a report state a count without also stating what it's
comparable against.

In-flight: none. No server/cluster touched this loop beyond the
pre-commit hook's pgbench smoke (its own private throwaway data dir,
`tmp/ralph-precommit-goopg-data-*`). No Go code changed, so no
tpch-spotcheck run this loop (not required — no production/planner code
touched, per AGENT.md's floor-measurement rule which only binds tasks
that change production code).

Next step: re-read the `## Current Priority` banner fresh in
`.ralph/fix_plan.md` (check date/content before trusting this note). Per
the banner as last read (2026-09-15 "Re-ordered" section), item 2's last
remaining piece is **M0137-0017** — "capture plans in BOTH serial and
parallel modes". It's flagged as mattering more than its size suggests
(TPC-H `parallelism` is currently unscoreable — `-serial` defaults true in
`estimate-audit`, so the headline 6/22 cannot score one of the nine
categories). Scope: capture with `-serial` and `-serial=false`, each
against its OWN PG baseline; the parallel-mode PG baseline does not exist
yet (`bench/tpch/plans-pg/` is serial-only, and per AGENT.md's
"Known-stale claims" K9 it is NOT a parity target to begin with — building
a live-PG parallel capture via `scripts/capture-tpch.sh`-style procedure,
or an `estimate-audit -serial=false` run against live `:65432`, is part of
this task). Once 0017 lands, item 2 is fully closed and the banner's next
unclaimed slot is item 3 (M0138 0007-0009 / M0140-0006).

Gates run: `go build ./...` clean (no Go files touched this loop). Python:
`python3 scripts/seam-decline-census.py --self-test` 4/4,
`python3 scripts/seam-decline-census-test.py -v` 7/7 ok, plus the live
cross-check above. Pre-commit hook's pgbench smoke PASS (tps ~43-149,
0 failed). `make ralph-state-guard`: one self-repair (same recurring
benign stale-clean-exit-marker pattern several prior loops have noted),
clean after repair.
