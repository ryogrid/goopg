(idle — nothing in flight)

Last landed: DU-002 slice 244 (loop #10) — ROLLBACK drops a composite type created
in the aborted transaction (the enum PendingCreatedEnums mechanism, mirrored for composites).

Why it was needed: slice 243 registered the composite unconditionally, so
`BEGIN; CREATE TYPE x AS (...); ROLLBACK;` left x alive (pg_type/virtual pg_class/\dT),
and a re-CREATE failed "already exists".

Mechanism (mirror of enum path):
- Context.PendingCreatedComposites + connTxState.PendingCreatedComposites (lowercased name set).
- execCreateType (operators_ddl.go composite branch) records the name when InExplicitTransaction.
- Two rollback paths each gain a "step 4": executor undoEnumDDLFromContext (operators_tx.go) and
  dispatch undoEnumDDLForRollback (dispatch.go) call InMemory.DropCompositeType per tracked name.
- Field threaded connTx↔ectx (dispatch.go ~252 / ~704) + cleared at every COMMIT/ROLLBACK exit
  (clearCtxTransaction, 5 dispatch transaction-verb exits, connTxState.reset).
- Heap pg_type/pg_class/pg_attribute rows carry the aborting XID → MVCC-invisible post-rollback;
  NO xmax stamp on rollback (matches enum behavior). Only in-memory registration is undone.

Files: internal/executor/context.go, operators_ddl.go, operators_tx.go;
internal/server/conn_tx.go, dispatch.go;
internal/executor/operators_tx_composite_test.go (+2 tests); docs/design/0110-0001 (Slice 244); fix_plan.md.

Gates: gofmt + go build ./internal/... clean; executor composite/enum/tx suites PASS;
live-verified on port 5544 (BEGIN/CREATE TYPE/ROLLBACK → gone, re-create OK, COMMIT persists +
pg_dump round-trip); pgbench pre-commit smoke on commit.

Next (slice 245+): composite fields of a USER-DEFINED type — parseCompositeFieldType
(pg18_user_catalog_rows.go) folds non-built-ins to text via TypeNameToOID; needs catalog
resolution of enum/domain/nested-composite field types. Then ALTER TYPE … ADD/DROP/ALTER ATTRIBUTE.
