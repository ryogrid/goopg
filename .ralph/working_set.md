Task: M0110-0003 (pg_amcheck) — loop #16. Extended the 002_nonesuch.pl port
(AC-002, already `port`) with two more faithful sections. COMMITTED-pending.

=== WHAT LANDED (this loop) ===
TestPort_PgAmcheck002Nonesuch now covers two later sections of the upstream .pl:
- the big `--no-strict-names` multi-pattern case (per-arg-kind warnings:
  no-heap-tables / no-btree-indexes / no-relations / no-connectable-databases,
  anchored by the existent `postgres.pg_catalog.pg_class` so exit stays 0);
- the cross-database "existent objects in the wrong databases" case
  (template1 / another_db / no_such_database; CREATE TABLE/INDEX + CREATE DATABASE).
Both PASS. No engine code changed — pure faithful test transcription + docs.
Files:
- internal/testport/pgamcheck002_port_test.go (+2 sections; header UPDATE note;
  inline comments documenting the two deferred sections at their call sites).
- docs/test-port/postgres-oracle-port-status.csv (AC-002 rationale extended);
  .md regenerated via `go run ./cmd/gen-oracle-port-status`.
- docs/design/0110-0003-pg-amcheck-tap-port.md (+ "002_nonesuch coverage
  extension" section incl. the two residuals).
- .ralph/deferral_ledger.md (loop #16 entry).
Gates: go build ./... clean; gofmt clean; vet clean; full internal/testport
pg_amcheck suite PASS (20.4s). TPC-H spotcheck N/A (no engine change, no data dir).

=== TWO DEFERRED RESIDUALS of 002_nonesuch.pl (NOT ported) ===
1. `datconnlimit = -2` invalid-database filter: needs a runtime pg_database
   shared-catalog write goopg lacks (UPDATE is a silent no-op; see memory
   goopg_no_runtime_shared_catalog_inplace_update). Separate capability.
2. `--exclude-schema` cases: NEW ENGINE BUG. pg_amcheck's exclude-CTE anti-join
   (`... LEFT OUTER JOIN exclude_pat ep WHERE ep.pattern_id IS NULL`) PANICS the
   backend. Pinned to the `toast` sub-CTE: a 5-col `exclude_pat` VALUES build
   relation gets a build-side filter carrying combined-join-schema column
   indices → `index out of range [43] with length 5` in MaterializedSlot.Get
   via joinOp.Open→drainRowsCtx→filterOp.Next→evalExprSlot. The 4-way `index`
   sub-CTE (same anti-join, relation on outer side) does NOT crash. Repro: see
   deferral ledger / design doc. Planner/executor column-remap defect; safe fix
   needs full TPC-H row-count gates (column-index bugs = most expensive failure).

=== NEXT STEP (resume) ===
Either: (a) fix residual #2 (the toast-CTE build-side column-index panic — a
GENERAL planner correctness fix worth its own gated loop; start at
operators_join_agg.go drainRowsCtx + how build-side filter predicate column
indices are assigned relative to the join's narrow build input); or (b) move to
another open milestone: M0095-0003 recvlogical (030, logical decoding, large);
M0110-0001 pg_dump 002 (catalog parity); M0110-0002 pg_waldump 002 (PG-format
heap WAL FPI); AC-003 remaining 003_check tiers (index AMs / types / TOAST
corruption) + 005_opclass_damage; M0117-0006/7/8 (Effort-L, defer).
