# Milestone 0121 — WordPress WP-CLI Verification Failure Remediation

**Status:** planned
**Filed:** 2026-07-02
**Depends on:** M0120 (supplies the triaged failure list + evidence)
**Reference plan:** `.ralph/fix_plan.md` (M0121 section)
**Source of truth:** the M0120 `report.md` + the `.ralph/deferral_ledger.md`
rows M0120 filed.

## Goal

Drive every WordPress WP-CLI operation that **FAILED** in M0120 to a correct,
verified PASS on goopg — by fixing the goopg bug or implementing the missing
capability — or, where the failure is genuinely a PG4WP/WordPress limitation
(not a goopg gap), by documenting that with evidence. When M0121 is done, the
full checklist passes on goopg (modulo explicitly-documented non-goopg
limitations), and each fix is protected by a regression test.

## Status / contract

- The task list is **seeded from M0120's triage** (one `M0121-NNNN` per
  `goopg-bug` / `goopg-missing` failure class) and grows if remediation
  uncovers further gaps.
- A task may only be checked off when the corresponding checklist item **passes
  its confirming read** on a fresh run and a regression test guards it.
- Each fix closes its M0120 `.ralph/deferral_ledger.md` row (`- → resolved`,
  citing the fix), per the deferral discipline.

## In Scope

1. **Per-failure remediation.** For each M0120 `goopg-bug` / `goopg-missing`
   failure: root-cause it against the captured SQL, fix goopg (never PG4WP,
   never a `goopg_compat` branch), and re-verify the checklist item passes.
   Likely areas, by analogy to root-0019…root-0022: unknown-literal/type
   coercion, SQL surface gaps (a statement/function/clause WordPress emits that
   goopg rejects), wrong SQLSTATE, catalog/`information_schema` visibility, and
   restart durability.
2. **Design docs.** Any non-trivial engine fix lands a
   `docs/design/0121-NNNN-*.md` (or a `root-00NN-*.md` for cross-cutting engine
   work) + a `docs/design/README.md` index entry, in the same loop.
3. **Regression tests.** Each behavior-changing fix adds a targeted test
   (`internal/…` unit or `internal/testport/` where a client is needed) so the
   WordPress-surfaced behavior cannot silently regress.
4. **Re-verification.** Re-run the affected checklist items (via
   `wp/verification/FLOW.md`) and refresh the evidence; update the M0120
   `report.md` verdicts to PASS.

## Out of Scope

- Failures triaged `pg4wp-limitation` or `harness` in M0120 — documented, not
  fixed in goopg (a PG4WP limitation may warrant a note in `wp/README.md`, but
  changing PG4WP or branching PG behavior is forbidden).
- New WordPress features/operations beyond the M0120 checklist.
- Broad refactors unrelated to a specific failing item.

## Definition of Done

1. Every M0120 `goopg-bug` / `goopg-missing` item now **passes** its confirming
   read on a fresh verification run.
2. Each fix has a design doc (when non-trivial) indexed in
   `docs/design/README.md` and a regression test.
3. Each corresponding `.ralph/deferral_ledger.md` row is `resolved` with a
   citation to the fix.
4. The M0120 `report.md` is refreshed: previously-failing items show PASS, or
   are annotated as a documented non-goopg limitation.
5. Pre-commit gates green for every fix (units + pgbench smoke hook; plus
   `scripts/tpch-spotcheck.sh` for any executor/planner/codec change, and the
   race gate for any concurrency-critical package).

## Required Design Docs

- Per-fix, as needed: `docs/design/0121-NNNN-<slug>.md` (reserve the `NNNN`
  when the task is picked; no bare `NNNN-*` placeholders) or a `root-00NN-*.md`
  for cross-cutting engine work, each indexed in `docs/design/README.md`.

## PostgreSQL References

- Each fix cites the upstream file it mirrors (`postgres/src/backend/…`) and is
  validated against vanilla PG 18.3 (`./postgres/local_install`,
  `scripts/pg-oracle-diff.sh` / `scripts/pg-regress-runner.sh`). Any divergence
  is fixed in goopg — never by branching PG.
