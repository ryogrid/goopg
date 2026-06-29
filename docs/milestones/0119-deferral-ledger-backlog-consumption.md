# Milestone 0119 — Deferral-Ledger Backlog Consumption (living milestone)

**Status:** planned
**Filed:** 2026-06-29
**Reference plan:** `.ralph/fix_plan.md` (M0119 section)
**Source of truth:** `.ralph/deferral_ledger.md`

> **This is a living milestone — its task list is never statically scoped.**
> Tasks are **appended to it over time** as `.ralph/deferral_ledger.md`
> accumulates new entries. Every ledger row whose `status` column is `-`
> (open / 未対応) is a candidate task for this milestone. The milestone is not
> "done" in the usual one-shot sense; it is the standing process by which goopg
> works the deferral backlog down to zero open rows, and it re-opens whenever a
> new deferral is recorded.

## Goal

Systematically *consume* (消化) the deferral-ledger backlog. The ledger records
every task that landed only partially (`landed / deferred / resume point / why`),
but historically there was no tracking of which deferrals were later resolved and
which are still open. The `status` column added alongside this milestone closes
that gap:

- `-` — open / 未対応: the deferred scope has not yet been consumed.
- `resolved` — the deferred scope has since landed (by a later ledger row, a
  promotion, or current code) and an M0119 task has verified it.

For each open (`status = -`) ledger row, this milestone drives it to closure:
either implement the deferred scope, or — where it turns out already done or no
longer applicable — verify that and mark it `resolved`. When genuinely open scope
must move to a different milestone, an explicit cross-referenced follow-up is
filed (never a silent forward reference).

## Status / ledger contract

- `.ralph/deferral_ledger.md` is the authoritative backlog. Its `status` column is
  the single tracking field for this milestone.
- An M0119 task may only set a row to `resolved` after **verifying** the deferred
  scope actually landed (cite the closing ledger row / promotion / code), or after
  implementing it.
- A new deferral appended by any loop starts at `-` and thereby enters this
  milestone's backlog automatically.

## In Scope

The currently-known open deferral themes, grouped by source task-id (seeded list —
**this set grows** as triage and new ledger entries surface more):

- **CLOG store swap, Part B** — M0117-0006 / M0117-0007 / M0117-0008. Live CLOG
  store swap (pool replaces banks); highest blast radius (Hard-won Rule #1).
- **initdb remaining options** — M0102-0010: `--encoding`, `--locale`/`--lc-*`/
  `--locale-provider`/`--icu-locale`, `--data-checksums` default-ON flip,
  `--allow-group-access`, `--auth*`/`--pwfile`, `--sync-method`/
  `--no-sync-data-files`, `--set`/`--text-search-config`.
- **pg_dump 002–010 TAP** — M0110-0001: schema dump, dump/restore round-trip,
  parallel, filter-file, connstr — gated on broad catalog-view parity.
- **pg_waldump server tier** — M0110-0002: `002_save_fullpage` + per-rmgr/relation/
  block filtering (needs PG-decodable FPI + index AMs).
- **pg_amcheck server tier** — M0110-0003: `002_nonesuch` … `005_opclass_damage`,
  `verify_heapam()` SRF + opclass catalog parity.
- **pg_basebackup recvlogical** — M0095-0003: logical decoding (`030 recvlogical`).
- **isolation residual** — M0118-0002 / M0118-0004: predicate-gin / predicate-gist
  AM-grain SSI and `deadlock-parallel` (parallel-worker lock groups). *Some of
  these may already be resolved per recent promotions — the M0119-0001 triage pass
  confirms current state and marks them `resolved` where so.*

## Out of Scope

- Re-litigating deferrals already verified `resolved`.
- Changing any PostgreSQL behavior to match goopg — divergence from the PG 18.3
  oracle is always a goopg bug to fix in goopg.
- New scope not traceable to a deferral-ledger row.

## Workflow Per Task

1. Pick the next open (`status = -`) ledger row(s) for a theme, using the row's
   `resume point` and the referenced design docs as the starting context.
2. **Write the design doc first** under `docs/design/<source-id>-NNNN-*.md` with
   status `draft`, and index it in `docs/design/README.md`.
3. **The design doc must pass an agent review** before implementation begins.
4. Implement against the reviewed design doc; run the gates the touched subsystem
   requires (per the practice cards / Hard-won Rules — e.g. TPC-H spot-check for
   executor/planner, `-race` for wal/mvcc).
5. Update the design doc to `accepted` when stable.
6. Flip the ledger row(s) `status` to `resolved` and tick the fix_plan item.

## Definition of Done

This milestone is *complete for now* when `.ralph/deferral_ledger.md` has **zero**
`status = -` rows — each either implemented or verified `resolved`, or explicitly
re-scoped to a tracked follow-up milestone/task. It re-opens whenever a new
deferral row is appended. Every consumed entry must have a reviewed design doc.

## Required Design Docs

Added per task as work begins — **created and reviewed by the agent that picks up
the task, not pre-created in this milestone**. The triage task (M0119-0001) is
documentation-only and is exempt from the design-doc requirement.

- `docs/design/0119-0004-nulls-not-distinct-enforcement.md` (M0119-0004) —
  `NULLS NOT DISTINCT` uniqueness enforcement at INSERT/UPDATE. Reviewed by an
  agent before implementation (heap-scan fallback; upsert/CREATE-INDEX surfaces
  deferred to a follow-up). Landed 2026-06-29 (loop #14).

## PostgreSQL References

Per-task: cite the upstream file mirrored for each consumed deferral in that
task's design doc (e.g. `postgres/src/backend/access/transam/clog.c` for the CLOG
store swap, `postgres/src/bin/pg_dump/` for pg_dump TAP). The deferred rows' own
`why` / `resume point` fields name the relevant subsystems.
