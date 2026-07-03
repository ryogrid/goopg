# M0120 — WordPress WP-CLI Verification: Aggregate Report

Aggregates the three per-run triage docs into one PASS/FAIL/class table across
all 40 `CHECKLIST.md` items (32 write + 8 read). Classification per FLOW.md §4.
Source runs:

- WP-01..16: `wp/verification/results/20260704-072755/summary.md` (M0120-0002)
- WP-17..32: `wp/verification/results/20260704-073700/summary.md` (M0120-0003)
- WP-R1..R8: `wp/verification/results/20260704-075221/summary.md` (M0120-0004)

## Summary

| | count |
|---|---|
| Total checklist items | 40 |
| Fully PASS (all sub-steps incl. confirming read) | 34 |
| FAIL — `goopg-bug` | 2 (WP-02, WP-03 — one root cause) |
| FAIL — `harness` (checklist/test error, not goopg) | 1 (WP-13) |
| FAIL — `pg4wp-limitation` | 3 (WP-32, WP-R7, WP-R8 partial) |
| FAIL — `goopg-missing` | 0 |

**Bottom line: exactly one goopg defect surfaced by the entire 40-item sweep**
— the WP-02/WP-03 backend panic (one root cause, two checklist items). Every
other FAIL is attributable to the checklist itself or to the vendored PG4WP
connector, not to goopg.

## Per-item table

| item | class | verdict | resume/reference |
|---|---|---|---|
| WP-01 post create | — | PASS | |
| WP-02 post update | `goopg-bug` | **FAIL** | panic in `evalFastExpr`/`Slot.Get`; ledger 2026-07-04 M0120-0002 row; **M0121-0002** |
| WP-03 post delete (trash) | `goopg-bug` | **FAIL** | same root cause as WP-02; **M0121-0002** |
| WP-04 post delete --force | — | PASS | |
| WP-05 post create (page) | — | PASS | |
| WP-06 post generate x20 | — | PASS | |
| WP-07 post meta add | — | PASS | |
| WP-08 post meta update | — | PASS | |
| WP-09 post meta delete | — | PASS | |
| WP-10 term create category | — | PASS | |
| WP-11 term create post_tag | — | PASS | |
| WP-12 term update category | — | PASS | |
| WP-13 post term set | `harness` | **FAIL** | checklist targets `pageID` with taxonomy `category`, which WP core never registers for `page` objects (fails identically on real WordPress); fix `CHECKLIST.md` to target a `post`-type object, not a goopg fix |
| WP-14 term meta add | — | PASS | |
| WP-15 term delete category | — | PASS | |
| WP-16 user create | — | PASS | |
| WP-17 user update | — | PASS | |
| WP-18 user meta update | — | PASS | |
| WP-19 user set-role | — | PASS | |
| WP-20 user delete --reassign | — | PASS | |
| WP-21 comment create | — | PASS | |
| WP-22 comment approve | — | PASS | |
| WP-23 comment meta add | — | PASS | |
| WP-24 comment delete --force | — | PASS | |
| WP-25 option add | — | PASS | |
| WP-26 option update (blogname) | — | PASS | |
| WP-27 option delete | — | PASS | |
| WP-28 option update (TOAST ~20000B) | — | PASS | root-0022 TOAST path exercised cleanly, no regression |
| WP-29 transient set | — | PASS | |
| WP-30 plugin activate hello | — | PASS | |
| WP-31 plugin deactivate hello | — | PASS | |
| WP-32 db query INSERT/UPDATE/DELETE | `pg4wp-limitation` | **FAIL** | `wp db query` shells to native `mysql` CLI against goopg's PG port — MySQL wire handshake fails before any SQL reaches goopg (confirmed via zero statement-log traffic); see `CHECKLIST.md` "Known non-goopg limitation" bullet 1 |
| WP-R1 post list --format=count | — | PASS | |
| WP-R2 post get 1 --field=post_title | — | PASS | |
| WP-R3 user list --format=table | — | PASS | |
| WP-R4 term list category --format=count | — | PASS | |
| WP-R5 comment list --format=count | — | PASS | |
| WP-R6 option get siteurl | — | PASS | |
| WP-R7 db query SELECT COUNT(*) | `pg4wp-limitation` | **FAIL** | identical failure mode to WP-32; see `CHECKLIST.md` bullet 2 |
| WP-R8 core version / db size --tables | `pg4wp-limitation` | **FAIL (partial)** | `core version` sub-step PASS; `db size --tables` sub-step returns zero rows because PG4WP's `ShowTablesSQLRewriter.php` builds its query in a single-quoted PHP string, so `$schema` is never interpolated — goopg correctly returns 0 rows for the literal (bogus) schema name; see `CHECKLIST.md` bullet 3 |

## Classification cross-reference

- **`goopg-bug` (M0121 scope):** WP-02/WP-03 → `.ralph/deferral_ledger.md`
  (2026-07-04, M0120-0002 row, `status: -`) → **M0121-0002** (fix_plan.md).
  This is the only open goopg-attributable failure from the whole sweep.
- **`harness` (checklist fix, not goopg):** WP-13 → documented above; a future
  loop should retarget `CHECKLIST.md`'s WP-13 step at a `post`-type object
  instead of `pageID`. No ledger row (not a goopg behavior gap).
- **`pg4wp-limitation` (out of scope per M0121's "never PG4WP" rule):** WP-32,
  WP-R7, WP-R8 (`db size --tables` sub-step) → all three documented in
  `CHECKLIST.md`'s "Known non-goopg limitation" section (3 bullets). No ledger
  rows (goopg's execution is correct given what it received, or it never
  received the request at all).

## Deferral ledger status

Exactly one ledger row required by this sweep, and it was already filed by the
M0120-0002 loop (2026-07-04, `status: -`, task-id `M0120-0002`, cross-referencing
`M0121-0002`). No additional ledger rows are needed by M0120-0005 — the other
four FAILs (WP-13, WP-32, WP-R7, WP-R8) are `harness`/`pg4wp-limitation`,
which FLOW.md §4 explicitly scopes to "document, not fix in goopg" and are
recorded in `CHECKLIST.md` instead of the ledger.

## Milestone disposition

M0120 (execute + capture, no engine fixes) is **complete**: all 40 checklist
items ran with full evidence capture (WP-CLI output + goopg statement log +
PG4WP SQL + a confirming read, including for passing items). The single
goopg-attributable defect is handed off to **M0121-0002**; M0121-0001 (seed the
remaining M0121 task list from this triage) needs no further seeding since
this sweep surfaced only the one already-seeded goopg-bug.
