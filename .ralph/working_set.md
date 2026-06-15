Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 25
(pg_partitioned_table empty view) COMPLETE this loop. NOTHING in flight; next
loop starts on slice 26 (empty pg_trigger virtual view).

=== DONE (loop #49) — DU-002 slice 25 ===
Added empty `pg_partitioned_table` virtual view (OID 3350) in
internal/catalog/catalog.go beside pg_range/pg_event_trigger. pg_dump's
partition-key probe (`SELECT partrelid FROM pg_partitioned_table WHERE (SELECT
c.oid FROM pg_opclass …) = ANY(partclass)`) now resolves instead of erroring.
goopg surfaces partition membership via pg_class.relkind='p'/'P' + pg_inherits,
NOT a per-partition-key heap, so 0 rows is correct; with 0 rows the
`= ANY(partclass)` predicate is never evaluated. Schema matches
pg_partitioned_table.h: partrelid oid, partstrat "char", partnatts int2,
partdefid oid, partattrs int2vector→int2[], partclass oidvector→oid[],
partcollation oidvector→oid[], partexprs pg_node_tree (int2vector/oidvector
represented as int2[]/oid[] per pg_index indkey/indclass convention).
VirtualRows returns nil (empty). Pure additive virtual view — zero existing
query/row-count risk.
Updated: catalog.go (view def), pgdump_connsetup_test.go (next-blocker header
→ pg_trigger), design doc 0110-0001 (slice-25 block), fix_plan loop #49 entry.
Gates: build/gofmt/vet clean; catalog suite PASS;
TestPort_PgDumpConnectionSetup PASS. tpch-spotcheck N/A (additive empty virtual
catalog view only; no physical/codec/executor change).

=== NEXT STEP — DU-002 slice 26 (pg_trigger empty view) ===
pg_dump now fails: `relation "pg_trigger" does not exist`. Query (getTriggers):
`SELECT t.tgrelid, t.tgname, pg_catalog.pg_get_triggerdef(t.oid, false) AS tgdef,
t.tgenabled, t.tableoid, t.oid, t.tgparentid <> 0 AS tgispartition FROM
unnest('{}'::pg_catalog.oid[]) AS src(tbloid) JOIN pg_catalog.pg_trigger t ON
(src.tbloid = t.tgrelid) LEFT JOIN pg_catalog.pg_trigger u ON (u.oid =
t.tgparentid) WHERE ((NOT t.tgisinternal AND t.tgparentid = 0) OR t.tgenabled !=
u.tgenabled) ORDER BY t.tgrelid, t.tgname`.
Add empty pg_trigger virtual view (pg_trigger.h, OID 2620) in catalog.go beside
pg_partitioned_table. goopg has no user triggers, so 0 rows is correct; the
unnest('{}') source is empty so the JOIN and pg_get_triggerdef are never
evaluated. Cols (pg_trigger.h): oid, tgrelid oid, tgparentid oid, tgname name,
tgfoid oid, tgtype int2, tgenabled "char", tgisinternal bool, tgconstrrelid oid,
tgconstrindid oid, tgconstraint oid, tgdeferrable bool, tginitdeferred bool,
tgnargs int2, tgattr int2vector→int2[], tgargs bytea, tgqual pg_node_tree,
tgoldtable name, tgnewtable name. RUN TestPort_PgDumpConnectionSetup to confirm
+ find next blocker empirically.

ORTHOGONAL PRE-EXISTING (track separately): reading a text[] column back from
the heap yields the BINARY array encoding (KindString raw bytes), not the text
repr expandArrayDatum parses. Irrelevant to empty pg_dump views.

Other open (larger, untouched): M0110-0003 AC-003 003_check feature tiers;
M0110-0002 002_save_fullpage; M0095-0003 recvlogical; M0117-0006/7/8 (Effort-L CLOG).
