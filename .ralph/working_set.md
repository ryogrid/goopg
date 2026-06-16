(idle — nothing in flight)

Last landed: DU-002 slice 118 (loop #82) — sequence `OWNED BY table.column` now
dumps its trailing `ALTER SEQUENCE ... OWNED BY ...;`. FIRST non-empty pg_depend:
pg_dump's getTables LEFT JOINs pg_depend (deptype IN 'a','i') to find owning_tab/col.
Fix: catalog.SeqParams.OwnedBy (filled from seqState.ownedBy) + new
InMemory.dependVirtualRows synthesizing the AUTO ('a') row (classid=refclassid=1259,
objid=seq OID, refobjid=table OID, refobjsubid=attnum=Ordinal+1, deptype='a').
Resolves owner against sequence's own schema for unqualified clauses.
GOTCHA: validateSeqOwnedBy splits OWNED BY on the FIRST dot, so a 3-part
`schema.table.col` mis-parses — fixture uses unqualified `OWNED BY owner_tbl.id`.
Files: internal/catalog/catalog.go, internal/executor/operators_sequence.go,
internal/testport/pgdump_connsetup_test.go, docs/design/0110-0001-pg-dump-tap-port.md.
Next direction (fix_plan loop #82 note): slice 119 — descending sequence
(`INCREMENT BY -1` → MINVALUE/MAXVALUE -1 default-suppression branch), or pivot to a
multi-statement pg_dump object surface beyond single sequences.
