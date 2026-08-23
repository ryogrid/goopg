Task just completed: M0134-0096 (brin_bloom.sql) — sized live against the PG
18.3 oracle (scripts/pg-regress-runner.sh --verbose brin_bloom, diff 205
lines): PARKED, NO CODE FIX this loop (pure sizing, confirmed as predicted
by the prior loop's baton). Confirmed cross-file with M0134-0095 (brin.sql,
same BRIN access-method family): shares every large blocker already
ledgered there — brin_summarize_range/brin_desummarize_range entirely
unimplemented, planner never selects Bitmap Heap Scan over a BRIN index by
correlation, `inet '.../nn' + int` typed-literal arithmetic is a bare
parser syntax error, `tid '(2,0)'` inside a PL/pgSQL `EXIT WHEN` fails to
parse, and the SAME `NULL array elements are not supported` (0A000)
coercion bug reproduces in this second independent file (was previously
un-root-caused; now confirmed real and cross-file, still not root-caused).
Two NEW bloom-specific gaps found this loop: (1) every
`..._bloom_ops(n_distinct_per_range=...)`/`(false_positive_rate=...)`
reloption is rejected outright with `operator class ... has no options` —
no per-opclass reloption plumbing for the bloom family at all (blocks all
4 of the file's bounds-validation cases from even reaching the code under
test). (2) bloom opclass filter-membership semantics (bit-per-value
probabilistic match vs BRIN minmax range) unaudited — blocked by (1).

CSV row flipped not-tried -> failed via make regen-testport. Ledger row
appended: .ralph/deferral_ledger.md, 2026-08-24, M0134-0096. fix_plan.md
M0134-0096 marked [x] (PARKED convention, matches M0134-0094/-0095
pattern). No design doc needed this loop (no engine code changed — pure
sizing/ledger work, same as the M0134-0093 clean-close precedent).

Committed 636e1161 and pushed to origin/regress-renumbering (confirmed).

Nightly filing: checked ci/logs/action-items.md at loop start — same run
(20260824-013441, sha e7495e712dda) as prior 2 loops, already filed
(AI-...-001 units/internal/executor regression, AI-...-002 AdvisoryLock
repeat). No new filing needed this loop.

NEXT LOOP: per the Current Priority banner in .ralph/fix_plan.md, continue
M0134 top-to-bottom — next unworked item is **M0134-0097 (brin_multi.sql,
status not-tried)**. First: `git log --oneline -1 origin/regress-renumbering`
to confirm 636e1161 landed. Then size brin_multi.sql live against
./postgres oracle via scripts/pg-regress-runner.sh --verbose brin_multi
(background, generous timeout; rm -rf tmp/regress-goopg-data
tmp/regress-diffs first if a prior run left them non-empty). Given
brin_multi.sql is a THIRD file in the same BRIN access-method family
(brin.sql, brin_bloom.sql both PARKED on brin_summarize_range/
brin_desummarize_range/planner-BRIN-selection), expect the SAME core
blockers again — if so this should again be a fast PARK-with-
cross-reference (to M0134-0095/-0096) rather than fresh sizing work; only
investigate genuinely NEW multi-range-specific gaps beyond what's already
ledgered. CAUTION carried forward: watch `ps -o rss= -C goopg` while any
regress file runs; kill -KILL promptly (never bare pkill -f) if RSS climbs
unbounded. Worth considering after brin_multi.sql: three consecutive PARKs
on the same brin_summarize_range/brin_desummarize_range gap may justify
actually IMPLEMENTING those two functions in a dedicated future loop
(cross-cutting fix that would unblock brin.sql/brin_bloom.sql/brin_multi.sql
simultaneously) rather than continuing to just ledger it — flag this to
whichever loop next has bandwidth for an implementation (not sizing) task.

Gates run: make check-testport-inventory PASS; make regen-testport ran
clean; make ralph-state-guard PASS (self-repaired the same recurring stale
progress.json completed-marker pattern as prior loops — standing benign
artifact, not a regression); pre-commit hook's pgbench smoke PASS
(337/620/12405 TPS across the 3 pgbench transaction types). No engine code
touched this loop so no go build/go test gate was needed beyond what the
pre-commit hook already runs.

In-flight: none. (tmp/regress-goopg-data, tmp/regress-diffs, and
/tmp/brin_bloom_run.log from this loop's oracle run were rm -rf'd/removed.)
