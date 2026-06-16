(idle — nothing in flight)

Last landed: DU-002 slice 117 (loop #81) — typed (`AS smallint`/`AS integer`) and
`CYCLE` sequences verified to dump byte-identically. Verification slice, NO
production code changed (executor already tracked dataType + seqcycle; catalog
already maps seqtypid 21/23/20 + threads seqcycle). Added 3 fixtures to
TestPort_PgDumpConnectionSetup with precise multi-line block assertions.
Next direction (fix_plan loop #81 note): slice 118 — sequence with `OWNED BY`
(pg_depend 'a' path + `ALTER SEQUENCE ... OWNED BY` emission), or a descending
sequence (`INCREMENT BY -1` → MINVALUE/MAXVALUE -1 default-suppression branch).
