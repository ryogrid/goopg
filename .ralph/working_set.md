Task: M0110-0001 / DU-002 — pg_dump catalog-view parity. Slice 26
(pg_trigger empty view) COMPLETE this loop. NOTHING in flight; next loop
starts on slice 27 (empty pg_rewrite virtual view).

=== DONE (loop #50) — DU-002 slice 26 ===
Added empty `pg_trigger` virtual view (OID 2620) in
internal/catalog/catalog.go beside pg_partitioned_table. pg_dump's
getTriggers probe (`SELECT t.tgrelid, t.tgname, pg_get_triggerdef(...) … FROM
unnest('{}'::oid[]) AS src(tbloid) JOIN pg_trigger t ON …`) now resolves
instead of erroring. goopg has no user triggers, so 0 rows is correct; the
unnest('{}') source is empty so the JOIN/pg_get_triggerdef never evaluate.
Schema matches pg_trigger.h: oid, tgrelid oid, tgparentid oid, tgname name,
tgfoid oid, tgtype int2, tgenabled "char", tgisinternal bool, tgconstrrelid
oid, tgconstrindid oid, tgconstraint oid, tgdeferrable bool, tginitdeferred
bool, tgnargs int2, tgattr int2vector→int2[], tgargs bytea, tgqual
pg_node_tree, tgoldtable name, tgnewtable name. VirtualRows returns nil.
Pure additive virtual view — zero existing query/row-count risk.
Updated: catalog.go (view def), pgdump_connsetup_test.go (next-blocker header
→ pg_rewrite), design doc 0110-0001 (slice-26 block), fix_plan loop #50 entry.
Gates: build/gofmt/vet clean; catalog suite PASS;
TestPort_PgDumpConnectionSetup PASS. tpch-spotcheck N/A (additive empty virtual
catalog view only; no physical/codec/executor change).

=== NEXT STEP — DU-002 slice 27 (pg_rewrite empty view) ===
pg_dump now fails: `relation "pg_rewrite" does not exist`. Query (getRules):
`SELECT tableoid, oid, rulename, ev_class AS ruletable, ev_type, is_instead,
ev_enabled FROM pg_rewrite ORDER BY oid`.
Add empty pg_rewrite virtual view (pg_rewrite.h, OID 2618) in catalog.go beside
pg_trigger. goopg has no user rules, so 0 rows is correct (ORDER BY oid over an
empty relation yields no rows). Cols (pg_rewrite.h): oid, rulename name,
ev_class oid, ev_type "char", ev_enabled "char", is_instead bool, ev_qual
pg_node_tree, ev_action pg_node_tree. RUN TestPort_PgDumpConnectionSetup to
confirm + find next blocker empirically.

ORTHOGONAL PRE-EXISTING (track separately): reading a text[] column back from
the heap yields the BINARY array encoding (KindString raw bytes), not the text
repr expandArrayDatum parses. Irrelevant to empty pg_dump views.

Other open (larger, untouched): M0110-0003 AC-003 003_check feature tiers;
M0110-0002 002_save_fullpage; M0095-0003 recvlogical; M0117-0006/7/8 (Effort-L CLOG).
