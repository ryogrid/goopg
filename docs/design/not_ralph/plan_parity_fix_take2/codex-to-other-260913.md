# Handoff: Q96 plan-parity investigation (2026-09-13)

## Current objective and priority

Continue the repository objective of making Goopg TPC-H/TPC-DS plans match
PG18.3. The active witness is TPC-DS Q96. The user explicitly stopped the
Datum-size investigation (R114); do not resume it unless the user explicitly
re-prioritizes it. The unresolved question is why Goopg's Q96 plan/election
does not match PG18.3.

The working branch is `plan-parity-with-pg-take2`. The latest pushed commits
are:

- `bf2e41458` — R117 result, temporary child-cost source removed;
- `da8af9488` — approved R118 scope/TODO entry.

The worktree contains substantial unrelated foreign WIP (submodules,
benchmarks, `.claude*`, and untracked artifacts). Preserve it. Never use
`git add -A`, `git reset --hard`, or broad cleanup. Stage explicit paths only.

## Read first

The authoritative progress and process are in:

- [`TODO.md`](TODO.md) — round dependencies and status;
- [`r111-q96-common-data-oracle-retry/REPORT.md`](r111-q96-common-data-oracle-retry/REPORT.md)
  — valid common-input oracle and the two forced SQL forms;
- [`r115-q96-nonspill-forced-order/REPORT.md`](r115-q96-nonspill-forced-order/REPORT.md)
  — normal `addHashJoinPath`/`hashJoinCost` is not reached by Q96's forced
  LATERAL route;
- [`r116-q96-legacy-join-attribution/REPORT.md`](r116-q96-legacy-join-attribution/REPORT.md)
  — both explicit joins are Hash/BuildRight and the final LATERAL node is a
  separate legacy/prebuilt route;
- [`r117-q96-legacy-child-cost/REPORT.md`](r117-q96-legacy-child-cost/REPORT.md)
  — exact lower-child ledger and artifact checksums;
- [`r118-q96-lower-join-producers/SCOPE.md`](r118-q96-lower-join-producers/SCOPE.md)
  — reviewed next task and its lifecycle/lineage requirements;
- [`AGENT.md`](../../../../AGENT.md) — memory cap, server lifecycle, plan/value
  gates, and Q96 handoff commands.

## What R117 established

Under the R111 common-data forced forms, the upper Hash Join's own legacy
display term is identical (`6580.57`). Its total difference is inherited:

| forced form | lower Hash Join | upper child total | upper total |
| --- | --- | --- | --- |
| hdem-first | rows `688465`, width `476`, total `14155.41` | `21040.18` | `27620.75` |
| store-first | rows `688081`, width `1104`, total `14079.69` | `21032.50` | `27613.07` |

The margin is therefore exactly `7.68` (`21040.18 - 21032.50`). The first
differing *selected/root-lineage legacy Join ledger* is the lower Hash Join,
not necessarily the globally first source-level difference. The forced orders
have different right relations and schemas by construction:

- shared `store_sales`: 719876 rows, width 428, total 7198.76;
- hdem-first right `household_demographics`: 7200 rows, width 48, total 72.00;
- store-first right `store`: 12 rows, width 676, total 0.12.

Thus do not claim that all scan costs are equal. R117 only found no
like-for-like/shared-scan pricing discrepancy and no evidence for a Datum-size
cause. Both forced forms return `266`.

The likely next producer loci are source-level candidates, not conclusions:

1. `internal/optimizer/cardinality.go`: `EstimateRows(*Join)` → `estimateJoin`
   computes child rows, pair selectivity, residual selectivity, bounds,
   saturation, and final rows.
2. `internal/optimizer/createplanjoin.go`: `joinInputs.publishedSchema` /
   `publishedLayout` and path-backed Join/Project construction write the
   published schema/layout.
3. `internal/optimizer/planner.go`: legacy direct constructors and the EXPLAIN
   recursive inner-plan boundary may produce the selected prebuilt route.
4. `internal/optimizer/plancost.go`: `TupleWidth(n.Output())` and legacy
   display cost consume already-constructed fields; they are not yet proven
   producers of the divergence.

## R118 implementation contract

R118 is measurement-only and has an approved scope. Before source work, keep
the scope dependency in `TODO.md`, then implement/review/commit/push in the
usual order. The diagnostic must be default-off, EXPLAIN-only, and behavior
neutral when disabled.

The producer-to-renderer identity must be explicit. `PlanWithSettings` receives
an outer `parser.ExplainStmt`, recursively calls `PlanWithSettings` for its
inner statement, and only then constructs `&optimizer.Explain{Child: inner}`.
The proposed sidecar lifecycle is:

1. create one statement-local sidecar at the outer EXPLAIN wrapper only;
2. pass it through the recursive inner planner where lower Joins are built;
3. assign deterministic construction IDs and record immutable scalar producer
   snapshots; record clone/rewrite successor edges;
4. after all rewrites and before wrapper construction, run one canonical (or
   explicitly equivalence-tested) final-tree census covering all renderer-
   visible wrappers, including Gather, GatherMerge, and shared occurrences;
5. freeze an immutable report keyed by final occurrence ordinal and attach it to
   the Explain plan; discard planner pointers/maps before return;
6. have TEXT and JSON renderers consume only the frozen report. Ordinary and
   nested non-wrapper statements must allocate no sidecar.

Do not use a process-global registry or pointer reuse. Add tests for recursive
EXPLAIN handoff, error/panic cleanup, concurrent disjoint sidecars, repeated
deterministic IDs, clone/rewrite handling, and agreement between frozen census
and TEXT/JSON renderer-visible occurrences. Every `estimateJoin` record must
include concrete route, all arithmetic inputs/outputs, rounding,
saturation/non-finite branch, and an equation-reproduction test. Schema lineage
must include ordered output position, expression/origin identity, source-table
identity, type/OID, typmod/collation where applicable, nullability/width inputs,
and final `TupleWidth`.

Stop and report without a production correction if mapping is zero/multiple,
a producer is unreachable, schema lineage has no single writer, or the
recorded equation fails to reproduce final rows/width.

## Reproduction environment

Bootstrap every session as prescribed by `AGENT.md`:

```bash
cd /home/ryo/work/goopg/goopg
export PATH="$PWD/postgres/local_install/bin:$PATH"
export LD_LIBRARY_PATH="$PWD/postgres/local_install/lib:${LD_LIBRARY_PATH:-}"
export GOOPG_GATHER_PATHS=top
export GOOPG_PARTIAL_AGG_PATHS=on
export GOGC=off
export GOMEMLIMIT=12GiB
```

Use the private data clone `/tmp/r111-goopg-q96` and port `5568`. Never use a
shared 6543x bench port for manual work. All server starts/driven workloads
must be memory-capped and foreground through `scripts/goopg-test-run.sh` with
a unique `GOOPG_CG_UNIT`; use `-listen` (not `-p`). Example:

```bash
go build -o /tmp/r118-goopg ./cmd/goopg
GOOPG_CG_UNIT=r118-q96 scripts/goopg-test-run.sh \
  /tmp/r118-goopg start -D /tmp/r111-goopg-q96 -listen 127.0.0.1:5568
# run psql/EXPLAIN controls in the foreground
/tmp/r118-goopg stop -D /tmp/r111-goopg-q96
```

Session settings for both forced forms are:

```sql
SET work_mem = '64MB';
SET join_collapse_limit = 1;
SET from_collapse_limit = 1;
```

The exact SQL inputs are `/tmp/r101-hdem-first.sql` and
`/tmp/r101-store-first.sql`; preserve them unchanged. The historical R117
capture used TEXT and JSON EXPLAIN OFF×2/ON×2. Its retained paths/checksums
and values are listed in the R117 report; regenerate fresh artifacts for R118.
The former temporary flag was `GOOPG_Q96_LEGACY_CHILD_TRACE=1`, but its source
was removed in `bf2e41458`; do not reintroduce it without reviewed R118 source.

## Commit discipline for the successor

Before any R118 source edit, the approved scope is already pushed in
`da8af9488`. Obtain review of the implementation design, then use
`git commit -n` and push. After measurement, remove all temporary diagnostic
source before writing `REPORT.md`, mark R118 DONE in `TODO.md`, run the
focused/unit gates, commit with `-n`, and push again. Keep the original goal
active: R118 only narrows the Q96 cause; it does not establish PG-wide plan
parity or complete the TPC-H/TPC-DS objective.
