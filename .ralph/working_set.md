Task: M0120-0003 — Execute + capture write items WP-17…WP-32 (user role/delete,
comments + comment-meta, options/transients incl. TOAST-sized WP-28, plugin
activate/deactivate, raw INSERT/UPDATE/DELETE via `wp db query`).

Files: wp/verification/driver_wp01_16.sh (reference pattern — WP-17..32 needs
its own driver script, e.g. driver_wp17_32.sh, sourcing run_item.sh the same
way); wp/verification/results/20260704-072755/ids.env has pageID=10/userID=2
to reuse as targets.

Key symbols: wp/verification/run_item.sh (run_item/baseline_snapshot),
wp/verification/CHECKLIST.md items WP-17..32 (§D cont'd, E comments, F
options/transients, G plugins).

Findings from M0120-0002 (just closed this loop):
- 13/16 WP-01..16 PASS against the reset schema.
- WP-13 FAIL is a harness/checklist bug (category taxonomy doesn't apply to
  `page` objects — CHECKLIST.md should target a `post`, not `pageID`). Not a
  goopg bug; leave for M0120-0005 to fix the checklist, don't fix mid-driver.
- WP-02/WP-03 FAIL is a real goopg-bug: backend panic (index out of range) on
  `post update`/`post delete` (trash) whenever the post has a default-category
  reassignment. Seeded as M0121-0002, ledger row appended 2026-07-04. Do NOT
  attempt the fix in an M0120 loop — M0120 is capture-only, M0121 fixes.
- WATCH: if WP-17..32 driver hits ANY item that goes through
  wp_set_post_categories/wp_set_object_terms (comments on a post could,
  unlikely for options/plugins), expect the same panic — that's a known
  duplicate of M0121-0002, not a new finding; don't re-ledger it, just note
  the item as blocked-by-M0121-0002 in the WP-17-32 summary.

Next step: write wp/verification/driver_wp17_32.sh (mirror driver_wp01_16.sh's
structure) covering WP-17..32 per CHECKLIST.md §D cont'd/E/F/G, run it with a
fresh RUN=wp/verification/results/$(date +%Y%m%d-%H%M%S) dir, write a
summary.md classifying each item PASS/goopg-bug/goopg-missing/pg4wp-limitation
/harness (same rubric as M0120-0002's summary.md), ledger any new goopg
failures, then mark M0120-0003 [x] in fix_plan.md.

Gates run this loop: `make ralph-state-guard` (must run before status block —
not yet run as of this note; run it next). No Go code changed this loop (pure
harness execution + fix_plan/ledger bookkeeping), so no build/test/pgbench
gates apply.
