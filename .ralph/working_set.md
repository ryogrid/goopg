Task: M0120-0004 — Execute + capture read items WP-R1…WP-R8 (post/user/term/
comment list/get/count, `option get siteurl`, raw SELECT via `wp db query`,
`db size`/`core version`).

Files: wp/verification/driver_wp17_32.sh (just-added reference — WP-R1..R8
needs its own driver, e.g. driver_wpr1_r8.sh, sourcing run_item.sh the same
way as the two prior drivers); wp/verification/results/20260704-073700/ids.env
has pageID=10 to reuse as a read target (userID/commentID from this run were
deleted by WP-20/WP-24, so don't reuse those IDs — use pageID and post ID 1,
the WP welcome post, per WP-R2).

Key symbols: wp/verification/run_item.sh (run_item/baseline_snapshot),
wp/verification/CHECKLIST.md §I (WP-R1..R8).

Findings from M0120-0003 (just closed this loop):
- 15/16 WP-17..32 items PASS (30/32 sub-steps incl. confirming reads).
- WP-28 (TOAST-sized 20000-byte option) round-tripped cleanly — no
  root-0022-class regression.
- WP-32 (`wp db query` raw INSERT/UPDATE/DELETE) FAILs identically on all 3
  sub-steps: **pg4wp/harness limitation, NOT a goopg bug.** WP-CLI's `db`
  command shells out to the native `mysql` CLI against `DB_HOST` (goopg's PG
  wire-protocol port), so the MySQL handshake fails before any SQL reaches
  goopg (confirmed: zero INSERT/UPDATE/DELETE in the goopg statement log for
  those steps — request never arrives). Documented in CHECKLIST.md's "Known
  non-goopg limitation" section (now 2 entries).
- **CRITICAL for next loop**: WP-R7 (`db query "SELECT COUNT(*) FROM
  wp_posts..."`) is the SAME `wp db query` mechanism and WILL fail identically
  (mysql-client handshake against goopg's PG port). Do NOT re-diagnose this as
  a new goopg bug or spend time debugging it — classify WP-R7 as
  pg4wp/harness limitation immediately, cite
  `wp/verification/results/20260704-073700/summary.md`, and move on.
- No new goopg bugs found this loop — no deferral-ledger row needed.
- Confirmed WP-02/WP-03 backend panic (index out of range) is still open as
  M0121-0002 (untouched this loop, capture-only per M0120 scope). WP-R1..R8
  are pure reads, so they should NOT trigger wp_set_post_categories — no
  expected overlap with M0121-0002, but if any read op unexpectedly touches
  it, note as blocked-by-M0121-0002 rather than re-ledgering.

Next step: write wp/verification/driver_wpr1_r8.sh (mirror the two existing
drivers' structure) covering WP-R1..R8 per CHECKLIST.md §I, run it with a
fresh RUN=wp/verification/results/$(date +%Y%m%d-%H%M%S) dir (goopg is
already running with GOOPG_LOG_STATEMENT=all — no restart needed), write a
summary.md classifying each item PASS/goopg-bug/goopg-missing/pg4wp-limitation
/harness (same rubric as prior summaries — WP-R7 pre-classified above), then
mark M0120-0004 [x] in fix_plan.md. After M0120-0004, M0120-0005 aggregates
report.md + final triage across the whole M0120 milestone.

Gates run this loop: `make ralph-state-guard` (ran, self-repaired a stale
"completed" progress marker, passed). Pre-commit pgbench smoke PASS (TPC-B
185 tps, simple-update 246 tps, select-only 14418 tps, 0 failed). No Go code
changed (pure harness execution + fix_plan/CHECKLIST bookkeeping).
