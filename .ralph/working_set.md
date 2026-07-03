Task: M0120-0005 — Aggregate `report.md` + final triage across the whole
M0120 milestone (WP-01..32 + WP-R1..R8, 40 items total).

Files: wp/verification/results/20260704-072755/summary.md (WP-01..16),
wp/verification/results/20260704-073700/summary.md (WP-17..32),
wp/verification/results/20260704-075221/summary.md (WP-R1..R8) — these three
are the source-of-truth per-run triage docs to fold into one report.md. Target
location: wp/verification/report.md (repo root of the verification dir, NOT
inside a results/<ts>/ subdir, since it aggregates across runs — check
FLOW.md/CHECKLIST.md for whether a specific path/filename is already
prescribed before picking one).

Key symbols: wp/verification/CHECKLIST.md (full 40-item table, "Known
non-goopg limitation" section now has 3 bullets), wp/verification/FLOW.md §4
(FAIL classification rubric: goopg-bug/goopg-missing/pg4wp-limitation/harness),
.ralph/deferral_ledger.md (append a row for every goopg-attributable FAIL).

Findings from M0120-0004 (just closed this loop):
- WP-R1..R6 + WP-R8's `core version` sub-step: 7/9 sub-steps PASS.
- WP-R7 (`db query "SELECT COUNT(*) ..."`) FAILs identically to WP-32 —
  confirmed pg4wp/harness limitation (mysql-CLI handshake against goopg's PG
  port), not a goopg bug.
- WP-R8's `db size --tables` sub-step FAILs (exit 0, but zero table rows) —
  **new finding**, root-caused to PG4WP's `ShowTablesSQLRewriter.php` using a
  single-quoted PHP string so `$schema` is never interpolated; goopg's
  response (0 rows for literal schema name "$schema") is correct. Pg4wp bug,
  not goopg. Documented as CHECKLIST.md's 3rd "Known non-goopg limitation"
  bullet.
- No new goopg bugs this loop, no deferral-ledger row added.

Full milestone tally so far (from the 3 summary.md files):
- WP-01..16 (M0120-0002): see that summary.md — includes the still-open
  backend panic bug **WP-02/WP-03 (index out of range)**, already filed as
  **M0121-0002** (capture-only per M0120 scope, do NOT re-fix here).
  Confirmed still-open (untouched) as of M0120-0004.
- WP-17..32 (M0120-0003): 15/16 items PASS (30/32 sub-steps); WP-32 FAIL
  (pg4wp/harness limitation, all 3 sub-steps).
- WP-R1..R8 (M0120-0004): 7/9 sub-steps PASS; 2 FAIL (WP-R7, WP-R8's `db size`
  sub-step), both pg4wp/harness limitations.

Next step: write wp/verification/report.md aggregating all 40 items
(pass/fail counts, the 3 pg4wp/harness-limitation entries cross-referenced to
CHECKLIST.md's "Known non-goopg limitation" section, and the one open goopg
bug WP-02/WP-03 cross-referenced to M0121-0002). Verify every goopg-attributable
FAIL (there should be exactly one: WP-02/WP-03) already has a
.ralph/deferral_ledger.md row — if M0120-0002's loop didn't add one (check
first), add it now per FLOW.md §4's required format, citing M0121-0002 as the
resume point. Then mark M0120-0005 [x] in fix_plan.md — this closes the entire
M0120 milestone.

Gates run this loop: `make ralph-state-guard` (ran, self-repaired a stale
"completed" progress marker again, passed). Pre-commit pgbench smoke PASS
(TPC-B 229 tps, simple-update 249 tps, select-only 14483 tps, 0 failed). No Go
code changed (pure harness execution + fix_plan/CHECKLIST bookkeeping).

Note: at loop start, the SessionStart hook flagged a possible concurrent
ralph_loop.sh writer. Investigated: it was a transient artifact of an
interactive session's push+fresh-screen-restart sequence that had already
completed by the time this loop began executing — only one `ralph` screen
(1636963) and one loop tree existed for the whole loop. No actual concurrency;
no tree corruption observed (git status was clean of foreign edits at start).
