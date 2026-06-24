Loop #22: M0118-0008 — `reindex-concurrently-toast` (LAST unpromoted spec; other
24 pass strict). Landed TOAST-exposure epic SLICE 4 (design 0118-0087). Spec
stays `defer` (slice 5 of the 5-slice epic remains).

## What landed (slice 4 of 5)
ALTER TABLE/INDEX … RENAME on a synthetic pg_toast relation/index under
allow_system_table_mods now works → the spec's global setup runs byte-for-byte.
Two blockers fixed:
- The synthetic toast rel/idx rows live ONLY in the virtual builders, not
  c.tables, so a rename can't mutate a real row. Record a NAME OVERRIDE instead:
  - NEW catalog field `toastRenames map[uint32]string` (synthetic OID → new name),
    init in ctor.
  - `toastDisplayNameLocked(oid, deflt)` (override or default; caller holds c.mu),
    `RenameToastRel(oid, newName)`, `LookupToastRel(schema, name) (oid, isIdx, ok)`
    (override OR default `pg_toast_<oid>[_index]` pattern, re-validates parent).
  - `ToastRelName` + BOTH pg_class synthetic rows (rel + idx relname) render via
    `toastDisplayNameLocked`. pg_index carries no names → unchanged.
- The ALTER INDEX parser had NO RENAME arm (fell into the no-op branch → empty
  Name). Added `ALTER INDEX … RENAME TO` arm emitting AlterTableRenameTable +
  Name. (ddl.go, right after the ATTACH PARTITION block.)
- Executor `execAlterTable`: when both table+index lookups miss and Name.Schema ==
  "pg_toast", a RENAME action resolves via LookupToastRel → RenameToastRel.

Inert when override map empty (every non-spec flow) ⇒ pg_class/pg_index/regclass
byte-identical, so no pg_dump/pg_amcheck re-run needed.

Files: internal/catalog/catalog.go; internal/parser/ddl.go (ALTER INDEX RENAME);
internal/executor/operators_ddl.go (pg_toast RENAME intercept);
internal/executor/toast_relation_exposure_test.go (NEW TestToastRelationRenameViaAlter);
docs/design/0118-0087-*.md + README; ledger.

## Next step (slice 5 — LAST)
`REINDEX {TABLE,INDEX} CONCURRENTLY pg_toast.<name>` routing. Probe divergence is
now EXACTLY at the REINDEX steps: `relation "pg_toast.reind_con_toast" does not
exist`. Plan: in reindexOp (internal/executor/operators_*.go — find via
`parseReindex`/`reindexOp.Next`), resolve a pg_toast schema-qualified target via
catalog.LookupToastRel (already built). The reindex itself is a catalog no-op on
the synthetic object BUT must wait for relation lockers on the PARENT table
(reind_con_wide) so the `<waiting …>`/`<... completed>` markers ride 0118-0029
`(*Context).waitForRelationLockers`. Handle DROP-during-reindex perms (parent
dropped → toast gone, sel2 errors `relation "reind_con_wide" does not exist`).
Then promote: switch TestPort_IsolationReindexConcurrentlyToast soft→strict, set
the D-002 CSV rationale, regen coverage/inventory md.

## Gates run (this loop)
go test ./internal/{catalog,parser}/ PASS; TestToastRelationRenameViaAlter +
slices 1-3 toast tests PASS; strict IsolationReindexConcurrently/ReindexSchema/
MultipleCic/DropIndexConcurrently1/AlterTable3 PASS; probe = global setup now
passes (divergence at REINDEX steps); go build clean; pgbench smoke = pre-commit.
