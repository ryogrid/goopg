(idle — nothing in flight)

M0120 milestone is fully CLOSED (0001-0005 all [x], committed). Next
recommended task: **M0121-0002** — fix the backend panic on `wp post
update`/`wp post delete` (trash, no `--force`)'s default-category
reassignment. Full repro, stack trace, and resume point are in
`.ralph/deferral_ledger.md` (2026-07-04, M0120-0002 row) and
`.ralph/fix_plan.md`'s M0121-0002 entry: panic is `runtime error: index out of
range [1] with length 1` in `Slot.Get` (`internal/executor/opnode.go:99`)
called from `evalFastExpr`'s `ExprColumnRef` case
(`internal/executor/exprnode.go:222`, colIdx=1) via `filterOpNext`
(`internal/executor/opnode.go:717`), triggered by
`wp_set_post_categories`'s `SELECT term_taxonomy_id FROM
wp_term_relationships WHERE object_id = ? AND term_taxonomy_id = ?` — a
3-column table (`object_id,term_taxonomy_id,term_order`); suspect a
filter/residual ColumnRef index not remapped to a narrower
projected/index-only-scan row. NOT reproducible via a single fresh isolated
psql connection running the same SQL text — likely depends on
session/plan-cache state built up by the preceding statement sequence in the
same connection (see `wp/verification/results/20260704-072755/WP-02/
goopg_statements.log` for the exact sequence to replay). This is a WAL/MVCC-
adjacent executor bug per the practice card — needs the race-gate + a
dedicated regression test, not a quick patch. Also note **M0121-0001**
(populate M0121 task list from M0120 triage) can be closed as a one-line
fix_plan tick in the same loop as M0121-0002 since `wp/verification/report.md`
confirms no other goopg-bug/goopg-missing failures were found — M0121-0002 is
the only seeded task needed.

Also worth a follow-up (low priority, harness-only, not goopg): WP-13 in
`wp/verification/CHECKLIST.md` targets `pageID` with the `category` taxonomy,
which WP core doesn't register for `page` objects — should be retargeted to a
`post`-type object. Not blocking, not ledgered (not a goopg gap).

Gates run this loop: `make ralph-state-guard` (ran, self-repaired the same
stale "completed" progress marker pattern as prior loops, then passed). No Go
code changed (pure harness aggregation + fix_plan bookkeeping), so no
build/test/pgbench gates apply beyond the state guard.
