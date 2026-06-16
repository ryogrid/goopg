Task: DU-002 slice 116 — surface sequences in pg_class (relkind='S', relam=0) so
pg_dump discovers + dumps them. COMPLETE, committing.

Files:
- internal/catalog/catalog.go — pg_class VirtualRows: added `!t.IsSequence` to the
  system-virtual skip; `relam` is now a var ("2" default, "0" for sequences);
  `IsSequence → relkind="S"` branch. Refreshed the now-stale pg_sequence builder
  comment that claimed sequences were hidden.
- internal/executor/operators_pg_get_sequence_data_test.go — TestSequenceSurfacedInPgClass
  (sequence appears in pg_class, relkind='S', relam=0).
- internal/testport/pgdump_connsetup_test.go — fixture: CREATE SEQUENCE plain_seq +
  num_seq(START 100 INCREMENT 10 MAXVALUE 1000); assertions for default-suppressed
  clauses, explicit params, and ABSENCE of OWNED BY (standalone seq).
- docs/design/0110-0001-pg-dump-tap-port.md — Slice 116 section.
- .ralph/fix_plan.md — loop #80 progress note.

Key symbols: pgClass VirtualRows (catalog.go ~L1913), relam, relkind, t.IsSequence,
RELKIND_HAS_TABLE_AM (PG excludes RELKIND_SEQUENCE → relam=0).

Findings: relam=0 is load-bearing — pg_amcheck's relation CTE only heap-verifies
relam=HEAP_TABLE_AM_OID, so relam=0 keeps the storage-less virtual sequence out of
verify_heapam (relam=2 would regress TestPort_PgAmcheck* which creates a sequence).
pg_dump getTableAttrs skips sequences (continue), so pg_attribute fidelity is moot.
Manual dump capture confirmed EXIT=0 + byte-identical output vs pg_dump 18.3, only
OWNER TO (no OWNED BY) for a standalone sequence.

Gates run: build+gofmt OK; catalog/planner/initdb/executor(full) unit PASS;
TestPort_PgDumpConnectionSetup PASS (2.29s, EXIT=0 verified); TestPort_PgAmcheck*
PASS (42.8s); pgbench pre-commit smoke on commit.

Next step: SLICE 117 — typed sequences (AS smallint/AS integer via seqtypid 21/23,
pg_dump emits `AS smallint`/`AS integer`) + a CYCLE sequence; or a sequence WITH
OWNED BY exercising the pg_depend 'a' path + `ALTER SEQUENCE ... OWNED BY` emission.
