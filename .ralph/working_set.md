(idle — nothing in flight)

Last loop (#26): M0119-0004 **REPLICA IDENTITY round-trip in pg_dump** —
LANDED DEFAULT/FULL/NOTHING (DU-002 slice 305). Design
`0119-0004-replica-identity.md`.

Fixed a pervasive latent bug: the heap pg_class row builder pg_dump reads
(`buildUserPGClassRow`, pg18_user_catalog_rows.go) HARDCODED
`relreplident='n'` (NOTHING; comment mislabelled "DEFAULT"), so EVERY dumped
table got a spurious `ALTER TABLE ONLY <t> REPLICA IDENTITY NOTHING;`. PG's
implicit default is `'d'`. Fix:
- catalog.go: new `ReplIdentOrDefault(s)` (empty→'d'); new
  `catalog.Table.ReplicaIdentity` field. Both pg_class builders (heap +
  VirtualRows sibling) route through it.
- parser: `AlterTableReplicaIdentity` action + `ReplicaIdentityMode`/`Index`
  fields parse `REPLICA IDENTITY {DEFAULT|FULL|NOTHING|USING INDEX name}`
  (FULL/NOTHING are KEYWORD tokens → accept both keyword + ident spellings).
- executor (operators_ddl.go): action sets tbl.ReplicaIdentity + flushes the
  pg_class HEAP row via delete-old-rows + syncTableToCatalogHeap (same path as
  SET STORAGE/COMPRESSION). USING INDEX ('i') REJECTED with 0A000 (deferred).

Gates: TestParseAlterTableReplicaIdentity + TestUserPGClassRowReplicaIdentity +
DU-002 slice 305 (ri_full→FULL, ri_nothing→NOTHING present; foo/bar/part
default→no clause) PASS vs real pg_dump 18.3; parser/catalog/executor PASS;
gofmt clean (hoisted long token to a `replIdent` local to avoid comment-column
churn); state guard consistent.

NEXT loop — remaining open under M0119-0004:
- **REPLICA IDENTITY USING INDEX** (relreplident='i'): pg_dump emits it at
  index-dump time keyed on `pg_index.indisreplident`, NOT at table-dump time.
  Resume = add `catalog.Index.IsReplicaIdentity`; set it (clear prior) in the
  'i' executor branch; report at indisreplident sites
  (pg18_user_catalog_rows.go:929 + catalog.go index VirtualRows ~5140/5176);
  re-sync index pg_index heap row; slice asserts `... REPLICA IDENTITY USING
  INDEX uidx;`. (deferral ledger has the full resume note.)
- pg_dump 002–010 catalog-view parity battery (further slices — find next gap).
- extended-protocol commit-time deferral (architecturally entangled).
Other M0119: M0119-0002 (CLOG store swap Part B — highest blast radius,
dedicated full-gate) / M0119-0005 (pg_waldump) / M0119-0006 (pg_amcheck).

NOTE (sibling paths): there are TWO pg_class row builders — VIRTUAL
(`catalog.go` VirtualRows) and HEAP (`buildUserPGClassRow`, pg18_user_catalog_
rows.go, written via syncTableToCatalogHeap). Per-table pg_class fields
(relpersistence/relpartbound/relreplident) must be carried in BOTH in sync;
a passing pg_dump slice does NOT disambiguate which one pg_dump reads (the
field drives both). Prior slice comments (166/167) claim pg_dump reads the
heap; the older memory `goopg_pg_class_virtual_pg_attribute_heap.md` claims
virtual — keep both builders consistent and the question is moot.
