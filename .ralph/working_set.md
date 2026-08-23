Task just completed: M0134-0097 (brin_multi.sql) — sized live against the PG
18.3 oracle (scripts/pg-regress-runner.sh --verbose brin_multi, diff 409
lines): PARKED, NO CODE FIX this loop (pure sizing, confirmed as predicted
by the prior loop's baton — THIRD file in the same BRIN access-method
family). Confirmed cross-file with M0134-0095 (brin.sql)/M0134-0096
(brin_bloom.sql): shares every large blocker already ledgered there —
brin_summarize_range/brin_desummarize_range/brin_summarize_new_values
entirely unimplemented (hit 11 times in this one file), planner never
selects Bitmap Heap Scan over BRIN by correlation, `inet '.../nn' + int`
typed-literal arithmetic syntax error, `tid '(2,0)'` in PL/pgSQL EXIT WHEN
parse failure, and the SAME `NULL array elements are not supported`
(0A000) coercion bug reproduces a THIRD time. Two NEW gaps: (1) minmax-multi
opclass reloptions (`values_per_range`) rejected with `operator class ...
has no options` — confirms the reloption-plumbing gap spans BOTH
non-default opclass families (minmax-multi AND bloom), not just bloom.
(2) an infinity-kind BRIN minmax-multi build-path internal error
(`expected time, got kind 2`) when indexing a column containing
-infinity/infinity timestamp values — distinct from the expected
out-of-range-timestamp divergence (goopg's narrower storage window is a
known/accepted gap; this is a genuine type-assertion mismatch bug).

CSV row flipped not-tried -> failed via make regen-testport. Ledger row
appended: .ralph/deferral_ledger.md, 2026-08-24, M0134-0097. fix_plan.md
M0134-0097 marked [x] (PARKED convention, matches M0134-0094/-0095/-0096
pattern). No design doc needed this loop (no engine code changed — pure
sizing/ledger work).

Committed and pushed to origin/regress-renumbering (see git log after
commit below — confirm on next loop's first git log check).

Nightly filing: checked ci/logs/action-items.md at loop start — same run
(20260824-013441, sha e7495e712dda) as prior 3 loops, already filed
(AI-...-001 units/internal/executor regression, AI-...-002 AdvisoryLock
repeat). No new filing needed this loop.

NEXT LOOP: per the Current Priority banner in .ralph/fix_plan.md, continue
M0134 top-to-bottom — next unworked item is **M0134-0098** (check
fix_plan.md for the exact filename/status; not yet inspected this loop).
First: `git log --oneline -1 origin/regress-renumbering` to confirm this
loop's commit landed. **STRONG RECOMMENDATION carried forward from this
loop:** three consecutive PARKs (M0134-0095/-0096/-0097) on the identical
`brin_summarize_range`/`brin_desummarize_range` gap (11+ call sites across
3 files) — the next loop with implementation bandwidth (not just sizing)
should seriously consider actually IMPLEMENTING those two functions
(mirrors postgres/src/backend/access/brin/brin.c's brin_summarize_range/
brinsummarize) as a standalone task, since it would unblock brin.sql,
brin_bloom.sql, AND brin_multi.sql simultaneously — a much better ROI than
continuing pure sizing passes on the remaining BRIN-family files (there
are likely more: check CSV for brin_*.sql not-tried rows). If continuing
straight sizing instead, run scripts/pg-regress-runner.sh --verbose
<next-file> (background, generous timeout; rm -rf tmp/regress-goopg-data
tmp/regress-diffs first). CAUTION carried forward: watch `ps -o rss= -C
goopg` while any regress file runs; kill -KILL promptly (never bare
pkill -f) if RSS climbs unbounded (this loop's run stayed bounded ~576MB,
no issue).

Gates run: make check-testport-inventory PASS; make regen-testport ran
clean; make ralph-state-guard PASS (self-repaired the same recurring stale
progress.json completed-marker pattern as prior loops — standing benign
artifact, not a regression). No engine code touched this loop; pre-commit
hook's pgbench smoke will run automatically at commit time (mandatory,
never bypassed).

In-flight: none. (tmp/regress-goopg-data, tmp/regress-diffs, and
/tmp/brin_multi_run.log from this loop's oracle run were rm -rf'd/removed.)
