# Milestone 0122 — Unimplemented-Feature Backlog Consumption (living milestone)

**Status:** planned
**Filed:** 2026-07-04
**Reference plan:** `.ralph/fix_plan.md` (M0122 section)
**Source of truth:** `unimplemented_feat.json` (repo root)

> **This is a living milestone — its task list is never statically scoped.**
> `unimplemented_feat.json` is a generated backlog of features goopg deferred
> across its commit history. Every entry whose `status` is `open` (未対応) is a
> candidate task for this milestone. Like M0119, it is not "done" in the usual
> one-shot sense; it is the standing process by which goopg works the
> unimplemented-feature backlog down to zero open entries, and it re-opens
> whenever a new deferral is recorded into the file.

## Goal

Systematically *consume* (消化) the unimplemented-feature backlog in
`unimplemented_feat.json`. That file was generated on **2026-07-02** by mining the
commit log (2597 commits, 2026-04-28..07-01) via a code-audit pipeline; its
`.unimplemented_features` array holds **181** entries, each a feature that was
deferred at some point (`feature` / `evidence` / `confidence` / `code_audit`).

**⚠️ The backlog is a snapshot and MAY LIST FEATURES THAT ARE ALREADY
IMPLEMENTED.** It was generated at a point in time and the audit was imperfect:

- **24 entries are uncertain** — `code_audit` is `unclear` (19) or absent (5): the
  auditor could not confirm the code path, so the feature may be partially or
  fully done already.
- **61 entries** have `resolution_check.ledger == "open"`: a matching deferral-
  ledger row may already track (or M0119 may already be closing) the same scope —
  potential duplicates.

Therefore **every M0122 task MUST first re-verify the feature against current HEAD
before writing any code.** If the feature already exists (grep/read the code, run
a probe against a live goopg, check the ledger / fix_plan / git log), the task
**marks the entry `resolved` and stops — it does NOT re-implement.** Only genuinely
still-missing scope is built. When genuinely open scope must move to a different
milestone, an explicit cross-referenced follow-up is filed (never a silent
forward reference).

## Status / tracking contract

- `unimplemented_feat.json` is the authoritative backlog. A per-entry **`status`
  field** is the single tracking field for this milestone:
  - `open` — 未対応: the feature has not yet been consumed/verified.
  - `resolved` — the feature has landed (implemented by an M0122 task) **or was
    already present** and an M0122 task verified it (cite the code / ledger row /
    commit that proves it).
- The `status` field is **added to every entry by the M0122-0001 triage task**
  (initialised from the re-verification pass); before that task runs, absence of
  `status` is treated as `open`.
- A new feature deferral appended to the file starts at `open` and thereby enters
  this milestone's backlog automatically.

## In Scope

The 181 entries cluster into the themed groups below (seeded list — **this set
grows / is refined** as the M0122-0001 triage pass and any new deferrals surface
more). Counts are approximate; each maps to a seeded `## M0122` fix_plan task.

- **Catalog system functions & pg_* view stubs** (~9) — `pg_relation_size`,
  `regexp_matches`, `pg_get_expr`, `isfinite`, `justify_*`, `pg_get_serial_sequence`,
  indexdef reconstruction. *Mostly small/mechanical — early quick-wins.*
- **EXPLAIN output & pg_stat instrumentation** (~7) — EXPLAIN XML/YAML, SETTINGS/
  BUFFERS rendering, `pg_stat_io` virtual table.
- **SQL language / executor features** (~21) — window frame ROWS/RANGE/GROUPS,
  GROUPING SETS/ROLLUP/CUBE, ANY/SOME/ALL, DEFAULT-clause parsing, intervals,
  BETWEEN SYMMETRIC, CTE-without-alias, WITH CHECK OPTION.
- **Types / opclasses / casts / collation / domains** (~11) — 1-byte `char`
  (OID 18) disambiguation, `pg_collation_for`, function-based cast dumping,
  ALTER TYPE RENAME/OWNER, domain CHECK renderer, `pg_ts_config` OIDs.
- **On-disk catalog persistence & shared catalogs** (~8) — persistent `pg_index`
  heap, index column ordering (ASC/DESC/NULLS) across restart, `pg_tablespace`
  visibility, `pg_database.datconnlimit` write.
- **DDL / admin commands / ctl / GUC config** (~14) — CREATE/DROP DATABASE full
  DDL, `goopg ctl restart`, REINDEX, SIGHUP config reload, tablespaces, ALTER
  FUNCTION/COLUMN, planner/jit GUC stubs.
- **Auth / roles / multi-DB isolation / encoding** (~6) — SASLprep / channel
  binding / `scram_iterations`, RBAC + `SET SESSION AUTHORIZATION`, encoding
  constraints during bootstrap/runtime.
- **WAL / recovery / crash-consistency infra** (~16) — WAL segment recycling,
  `WALInsertLock` array (parallel inserts), MultiXact WAL, `pg_subtrans`
  truncation.
- **Concurrency: buffer pool & btree locking** (~17) — Lehman/Yao crab-walk,
  `splitMu` removal, storage-pool pin-count race, re-enable the `-race` gate.
  *Large.*
- **Query optimizer & TPC-H/HammerDB correctness** (~17) — anti/semi-join
  unnesting, Q8/Q9/Q21 fixes; several blocked on the slot/TupleSlot pipeline.
- **Perf infra: vectorization / slot-pipeline / harness** (~19) —
  Borrow-semantics allocation rewrite, plannode migration, vectorized
  FilterOp/SeqScanOp, plan cache, HammerDB SF1 validation. *Architectural/epic.*
- **Physical/streaming replication & standby** (~10) — the streaming-replication
  epic (~25 sub-items), cascading replication, `STANDBY_SNAPSHOT_READY`. *Epic/
  blocked.*
- **Logical replication / decoding / subscription** (~11) — pgoutput DELETE
  identity, subscriber apply worker, DDL replication. *Epic; blocked on logical
  decoding (tracks D-004; overlaps M0119-0007).*
- **Test-suite porting: amcheck / verify_heapam / pg_dump** (~8) — **overlaps
  M0119-0004/0006**; the triage assigns each item to exactly one milestone.
- **Small / residual** (~7) — TOAST compression, autovacuum, FDW / HANDLER-VALIDATOR
  stub, GIST index support, LANGUAGE C — folded into the nearest cluster by triage.

## Out of Scope

- Re-implementing features that the triage verifies are already present (mark
  `resolved`, cite the proof).
- Any entry the deferral ledger / M0119 already tracks — deduped to a single owning
  milestone, not worked twice.
- `contrib` extensions explicitly out of scope (feature #124).
- Changing any PostgreSQL behavior to match goopg — divergence from the PG 18.3
  oracle is always a goopg bug to fix in goopg.

## Workflow Per Task

1. Pick the next cluster (or the highest-value open entries within it). **Re-verify
   each candidate entry against current HEAD first** — grep/read the code, probe a
   live goopg, check `.ralph/deferral_ledger.md` / `.ralph/fix_plan.md` / `git log`.
2. **If the feature is already implemented or no longer applicable**, set the
   entry's `status` to `resolved` (cite the code/ledger/commit that proves it) and
   stop — do NOT re-implement.
3. Otherwise **write the design doc first** under `docs/design/<id>-NNNN-*.md` with
   status `draft`, and index it in `docs/design/README.md`.
4. **The design doc must pass an agent review** before implementation begins.
5. Implement against the reviewed design doc; run the gates the touched subsystem
   requires (practice cards / Hard-won Rules — TPC-H spot-check for executor/
   planner, `-race` for wal/mvcc, etc.).
6. Update the design doc to `accepted`, flip the entry's `status` to `resolved`,
   and tick the fix_plan item.

## Definition of Done

This milestone is *complete for now* when `unimplemented_feat.json` has **zero**
`open` entries — each either implemented or verified `resolved` (already present /
no longer applicable), or explicitly re-scoped to a tracked follow-up
milestone/task. It re-opens whenever a new feature deferral is appended. Every
consumed (implemented) entry must have a reviewed design doc.

## Required Design Docs

Added per task as work begins — **created and reviewed by the agent that picks up
the task, not pre-created in this milestone**. The triage task (M0122-0001) is
documentation-only (it re-audits the backlog and populates `status`) and is exempt
from the design-doc requirement.

## PostgreSQL References

Per-task: cite the upstream file mirrored for each consumed feature in that task's
design doc (e.g. `postgres/src/backend/optimizer/` for the join-unnesting items,
`postgres/src/backend/replication/logical/` for logical decoding,
`postgres/src/backend/access/nbtree/` for the B-tree crab-walk). Each entry's
`evidence` / `feature` fields and its `source_commits` name the relevant
subsystems.
