# M0134-0021 — per-relation VACUUM/ANALYZE permission check (partition children)

Status: implemented (slice 1 of the `vacuum.sql` bucket set; case remains PARKED)
Task: `.ralph/fix_plan.md` M0134-0021 (`vacuum.sql`)
Oracle: PG 18.3 under `./postgres/`

## Problem

`VACUUM <partitioned-table>` / `ANALYZE <partitioned-table>` issued by a role that
owns neither the parent nor (some of) the children must emit one
`permission denied to vacuum "<rel>", skipping it` WARNING **per skipped
relation** — parent and each expanded partition child independently. goopg
checked ownership only for the explicitly-named relation, so every expanded
child was vacuumed/analyzed unconditionally and its WARNING was missing.

Measured at HEAD: `scripts/pg-regress-runner.sh --verbose vacuum` FAILS —
496 diff lines, 18 `+ERROR`, 16 `-ERROR`. This bucket accounts for 18 changed
lines, all pure omissions (`-` only, no text/order mismatch), across the six
ownership-permutation blocks at `postgres/src/test/regress/expected/vacuum.out:593-684`.

## PG's actual control flow (this is the part that decides the fix site)

`expand_vacuum_rel()` (`postgres/src/backend/commands/vacuum.c:899-1046`) checks
permission **only on the explicitly named relation** (`:974`) and then appends
partition children to the target list with *no* check — the comment at
`vacuum.c:1003-1005` says so verbatim ("we do not yet check the ownership of the
partitions/tables, which get added to the list to process. Ownership will be
checked later on anyway").

The per-relation check happens **after expansion, at relation-open time**, once
per flattened target: `vacuum_rel()` (`vacuum.c:2124`) and `analyze_rel()`
(`postgres/src/backend/commands/analyze.c:156`), each calling
`vacuum_is_permitted_for_relation()`. A denied relation is skipped alone; the
parent and sibling children still proceed.

## Design — two tiers, not one

Implementation note (corrected during implementation, FAIL-pre verified): a
"main-loop-only" check does NOT work, because in goopg a partitioned parent
never lands in the flattened target list at all (it has no storage — the
expansion closure replaces it with its children). PG has the same shape:
`vacuum_is_permitted_for_relation` emits the WARNING itself as a side effect and
a denial at `vacuum.c:974` **excludes the named relation from the flattened
list**, so `vacuum_rel`'s own call at `:2124` never runs for a denied *named*
target. The two calls are therefore two tiers, not one check in two candidate
places. The oracle case that proves it is
`postgres/src/test/regress/expected/vacuum.out:646-648` ("Only one partition
owned by other user": warnings for the parent AND child2, but not the permitted
child1).

Tier 1 — **expansion time, explicitly named relation only**
(`expandVacuumTargets` / `expandAnalyzeTargets`): keep the pre-existing
`maintenancePermitted` check and its WARNING (mirrors `vacuum.c:974`). Changed
behavior: on denial, a partitioned parent's children are **still expanded**
before `continue`, via a new `expandChildren(tbl)` closure factored out of the
`add()` recursion. PG appends children unconditionally regardless of the named
relation's ownership result (`vacuum.c:1003-1005`: "we do not yet check the
ownership of the partitions/tables ... Ownership will be checked later on
anyway"), so a denied parent must still yield independent per-child WARNINGs.

Tier 2 — **main per-target execution loop over the flattened list**
(`vacuumOp.Next` / `analyzeOp.Next`): a new `maintenancePermitted` check +
WARNING + `continue`, mirroring `vacuum_rel()` (`vacuum.c:2124`) and
`analyze_rel()` (`analyze.c:156`). In practice this only ever *fires* for
expanded partition children — a denied named relation was already excluded by
tier 1 — so there is exactly one WARNING per relation and no double-fire. A
permitted named relation is re-checked here, which is exactly what PG does too.

Both `internal/executor/operators_vacuum.go` and
`internal/executor/operators_analyze.go` change together (Hard-won Rule #2);
the analyze wording is `permission denied to analyze %q, skipping it`.

Tier 2 is gated on a non-empty explicit target list so the database-wide
`VACUUM;` / `ANALYZE;` arms are untouched: PG filters those **silently** via
`get_all_vacuum_rels` (`vacuum.c:1082`, plain `continue`, no WARNING), goopg's
bare `ANALYZE;` already filters silently, and goopg's bare `VACUUM;` filters not
at all — a pre-existing asymmetry this slice deliberately does not change
(ledgered).

The message plumbing needed no change: Go's `%q` on an ordinary identifier is
byte-identical to PG's `\"%s\"` (`vacuum.c:758-760`, `:771-773`).

## Also landed: two inert GUC registrations

`maintenance_work_mem` and `vacuum_truncate` were entirely unregistered, so
`SET`/`RESET` of them raised `unrecognized configuration parameter` (4 `+ERROR`
lines). Registered in `internal/utils/misc/defaults.go` following the `work_mem`
pattern, with PG 18.3 defaults/contexts from
`postgres/src/backend/utils/misc/guc_tables.c:2147,2593`. These are **inert** —
goopg's VACUUM never physically truncates trailing pages regardless of options
(see the comment at `internal/parser/parser.go:1947-1953`), so `vacuum_truncate`
is accepted and stored but does not yet steer behavior. Ledgered.

## Scope explicitly excluded

- `vac_truncate_test`'s `pg_relation_size()` assertions — a separate, untriaged
  relation-size/truncate gap that diverges *before* the GUC statement runs.
- Bare `VACUUM;` (database-wide) has no ownership check at all in goopg, while
  bare `ANALYZE;` skips silently without PG's WARNING — both asymmetries are
  unexercised by this case. Ledgered.
- Buckets A (option-literal grammar), B (`VACUUM ONLY`), D (ANALYZE reentrancy
  guard), E (option range/conflict validation), F (PROCESS_TOAST/VACUUM FULL
  toast filenode) — see `.ralph/deferral_ledger.md`.
