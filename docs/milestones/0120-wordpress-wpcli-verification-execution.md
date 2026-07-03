# Milestone 0120 — WordPress WP-CLI Verification Execution & Evidence Capture

**Status:** planned
**Filed:** 2026-07-02
**Depends on:** root-0023 (statement/query logging — the goopg-side capture
mechanism), root-0019…root-0022 (the WordPress-driven engine fixes already
landed).
**Reference plan:** `.ralph/fix_plan.md` (M0120 section)
**Artifacts:** `wp/verification/CHECKLIST.md`, `wp/verification/FLOW.md`

## Goal

Establish a **systematic, repeatable, evidence-backed** verification that
WordPress operations behave correctly on goopg. Run the 40-item WP-CLI
checklist (`wp/verification/CHECKLIST.md`, 32 write + 8 read) against the live
WordPress-on-goopg stack, capturing for **every** item — passing ones included —
the three evidence streams defined in `wp/verification/FLOW.md`:

1. WP-CLI stdout/stderr + exit code and the PG4WP SQL log (the rewrite + the
   exact SQL sent to goopg),
2. the goopg statement log (`GOOPG_LOG_STATEMENT=all`, root-0023) with `proto`
   and transaction `xid`,
3. a confirming `SELECT` proving the DB state changed as expected.

Produce a PASS/FAIL report and triage every failure into a root-cause class,
handing the goopg failures to M0121.

## In Scope

1. **Harness & pre-run setup.** Stand up capture per FLOW.md: restart the wp
   goopg instance with `GOOPG_LOG_STATEMENT=all` (through the memory cap,
   `GOOPG_CG_UNIT=goopg-wp`), enable PG4WP debug logging, snapshot baseline
   counts. A small run script implementing FLOW.md §2 (`run_item`).
2. **Execute the write items (WP-01…WP-32).** Posts/pages, post/term/user/
   comment meta, taxonomy, users/roles, comments, options/transients, a
   TOAST-sized option value, plugins, and raw INSERT/UPDATE/DELETE through
   PG4WP. Each with its confirming read.
3. **Execute the read items (WP-R1…WP-R8).**
4. **Evidence capture & storage.** One directory per item under
   `wp/verification/results/<timestamp>/` (git-ignored), plus a curated
   `report.md` summary table.
5. **Failure triage.** Classify each FAIL as `goopg-bug` / `goopg-missing` /
   `pg4wp-limitation` / `harness` (FLOW.md §4), using the captured goopg
   statement + PG4WP SQL as the anchor. For every `goopg-bug`/`goopg-missing`,
   append a `.ralph/deferral_ledger.md` row and file the matching `M0121-NNNN`
   follow-up task (the M0120→M0121 handoff).

## Out of Scope

- **Fixing** any failing operation — that is M0121. M0120 stops at a triaged,
  evidence-backed report.
- Browser / wp-admin UI testing, REST API, multisite, and third-party plugins
  beyond core-bundled ones (Hello Dolly, default themes).
- PG4WP-rooted failures (e.g. `information_schema` translation) — recorded as
  `pg4wp-limitation`, not driven to a goopg fix.
- Performance/load testing (covered by pgbench/TPC-H gates elsewhere).

## Definition of Done

1. All 40 checklist items executed against the live stack.
2. Each item has a captured evidence directory containing the WP-CLI
   output/exit, the goopg statement-log slice, the PG4WP SQL slice, and the
   confirming read — **including for items that passed**.
3. A committed/attached `report.md` gives a per-item PASS/FAIL verdict.
4. Every FAIL is classified into one of the four triage classes with the
   evidence cited.
5. Every `goopg-bug`/`goopg-missing` FAIL has a `.ralph/deferral_ledger.md`
   row (upstream PG citation + resume point) and a cross-referenced
   `M0121-NNNN` task, so the handoff to M0121 is unambiguous from both sides.

## Required Design Docs

- No new engine design doc (execution/verification milestone). The enabling
  mechanism is documented in `docs/design/root-0023-statement-query-logging.md`;
  the procedure lives in `wp/verification/FLOW.md`. Any goopg fix that M0120's
  triage discovers gets its design doc under M0121.

## PostgreSQL References

- Comparisons normalise against vanilla PG 18.3 (`./postgres/local_install`):
  where a WP-CLI operation's result is in doubt, run the same underlying SQL
  against PG 18.3 (`scripts/pg-oracle-diff.sh`) — any divergence is a goopg bug.
- WordPress schema/queries observed via the PG4WP rewriters under `wp/pg4wp/`
  and the captured `pg4wp_*.log`.
