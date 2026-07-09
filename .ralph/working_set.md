Task: M0122-0007 4d-ii-part-2b item 1, follow-up 16 — closed the ~50-site
cross-file `IndexesOnTable` sweep (except one deliberately deferred corner,
applyworker.go). COMPLETE and committed this loop. NIGHTLY TRIAGE checked
first: ci/logs/action-items.md's only item (AI-20260710-011513-001) already
has [x] tasks in fix_plan.md from two prior loops — nothing new to triage.

Files: internal/catalog/catalog.go (new `SearchPathCatalog.IndexesOnTable`
override — the single highest-leverage fix, transparently covers all 6
`internal/planner` call sites), internal/executor/{operators_fk,
operators_cluster,operators_reindex,deferred_unique,context,
operators_vacuum,operators_upsert,ssi,operators_storage,operators_ddl,
expr}.go (dbOid threaded through every remaining `IndexesOnTable` call;
expr.go's buildForeignKeyDefString gained a variadic dbOid param),
internal/executor/operators_upsert_test.go (new
TestPlanUpsertDoNothingNoTargetFindsArbiterUnderDistinctDBOid, verified to
fail against a revert of the SearchPathCatalog.IndexesOnTable addition),
docs/design/0122-0018-per-database-catalog-namespace.md (item 1 marked
landed except applyworker.go), docs/design/README.md (row extended),
.ralph/fix_plan.md (follow-up 16 entry), .ralph/deferral_ledger.md (new row:
applyworker.go corner + item 2 RelFileNode.DBOid as next resume points).

Key symbols: catalog.SearchPathCatalog.IndexesOnTable (internal/catalog/
catalog.go, next to the existing LookupTable/LookupIndex overrides) —
mirrors effectiveDBOid; before this loop SearchPathCatalog had NO override
for IndexesOnTable at all, so every internal/planner caller (which reaches
the catalog only via ctx.PlanCatalog/ctxPlanCatalog, always a
SearchPathCatalog per the prior loop's fix) silently promoted straight to
the embedded InMemory.IndexesOnTable with no dbOid — always DefaultDBOid.
resolveDefaultDoNothingArbiter (internal/planner/planner.go:8103) is the
concrete bug this exposed: bare `ON CONFLICT DO NOTHING`'s implicit
arbiter-index resolution.

Hypothesis/Findings (confirmed): re-measured via `grep -rn
'\.IndexesOnTable(\|\.AllUserViews(\|\.AllUserMatViews(' internal/` at loop
start (~57 sites) and again at the end (only applyworker.go:662 and the
planner.go/nl_index_join.go sites remain — the latter are now covered
transparently by the SearchPathCatalog override, confirmed by reading
Catalog interface + all 6 call sites' `cat` provenance). operators_sequence.go
and operators_pg_get_publication_tables.go (named in the design doc's file
list) turned out to have ZERO IndexesOnTable/AllUserViews/AllUserMatViews
call sites — no work needed there. applyworker.go was deliberately left
alone: its sibling w.cat.LookupTable (line 217) also has no dbOid arg and
NewApplyWorker never receives a per-subscription-dbOid-seeded catalog, so
the whole apply-worker path is uniformly un-migrated — threading only
primaryKeyOnlyRow would be a partial, inconsistent fix.

Gates run (all PASS): go build ./... clean; go vet ./... clean; go test
-race -count=1 ./internal/catalog/... ./internal/executor/...
./internal/planner/...; go test -short -count=1 $(go list ./... | grep -v
/internal/testport) (full repo, short mode, ~5 min, internal/initdb takes
the bulk at 261s); scripts/tpch-spotcheck.sh (Q12=2/Q13=33);
RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh (0 failed, all 3
pgbench workloads); make ralph-state-guard OK (one auto-repair:
progress.status reconciled from the previous loop's clean-exit "completed"
marker to "in_progress" — not a project-completion signal, same
false-positive pattern as prior loops).

In-flight: none. Note: analysis/tpch-explain-baseline.md, ci/logs/*, and
.ralph/progress.json show as modified but were deliberately left OUT of
this loop's commit (same as every recent prior loop) — auto-regenerated/
external-process artifacts. `postgres` shows as untracked content
(submodule) — pre-existing, not touched.

Next step for a future loop: **4d-ii-part-2b's item 1 applyworker.go corner
and item 2 are the next resume points.**
  1. applyworker.go: give ApplyWorker a per-subscription dbOid concept
     (there is none today — NewApplyWorker receives a bare
     `cat catalog.Catalog`), then thread it through primaryKeyOnlyRow
     AND w.cat.LookupTable (line 217) together, not piecemeal — see
     deferral ledger's 2026-07-10 item-1 row for the exact reasoning.
  2. Item 2 — RelFileNode.DBOid wiring at creation time (needs the
     postgres/template1 dual-mirror audit flagged in the design doc's
     "Blast radius" section: NewInMemory's `dbOid: DefaultDBOid` seed +
     the base/1/ + base/5/ mirror).
  3. Then 4e (cross-cutting fixups + the actual CREATE DATABASE ... TEMPLATE
     copy mechanism this whole epic exists to unblock).
Re-run the full catalog/executor/planner/server + short-mode whole-repo
suites plus tpch-spotcheck/pgbench-smoke gates after each sub-piece.
**Also**: before picking any milestone work, re-check
`ci/logs/action-items.md` for new `## AI-` items per the M-NIGHTLY
standing-priority rule.
