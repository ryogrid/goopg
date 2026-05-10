# Milestone 0078 — pgbench-compare re-validation post-M0079 catalog fix

**Status:** planned
**Depends on:** M0079-0001 (catalog DDL WAL recovery — landed)
**Drives:** Confirm the goopg vs PostgreSQL pgbench numbers under
the same parameters documented in `bench/pgbench-compare/README.md`
now that the persistent-index bug is fixed.

## Context

The pgbench measurement that surfaced M0079-0001 produced 0.86 TPS
on `goopg standard` workload (down from ~60 TPS pre-M0077) because
`pgbench_accounts_pkey` disappeared from the catalog after a
non-graceful restart. M0079-0001 closed the catalog persistence
gap; the re-measurement is pending.

This milestone re-runs the three pgbench workloads (`standard`,
`simple-update`, `select-only`) against the post-M0080 binary and
records results.

## Required design docs

To be picked up when the milestone is started:

- `docs/design/0078-0001-pgbench-revalidation-protocol.md`
  (protocol for the re-run; what counts as parity / regression
  vs PG baseline).

## Tasks

Tasks will be detailed when this milestone is picked up. See the
fix_plan.md note at the top of this file.

## Definition of Done (sketch)

- Three pgbench workloads (100c / 100t / 180s / scale=100) run
  to completion against a fresh-init goopg cluster.
- Results saved under `bench/pgbench-compare/results/`.
- Comparison report generated under `analysis/`.
- The 0.86 TPS regression is resolved (or, if not, root cause is
  identified and recorded).
