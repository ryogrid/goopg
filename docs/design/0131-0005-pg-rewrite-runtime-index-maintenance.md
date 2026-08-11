# pg_rewrite runtime index maintenance — a hosted PG can finally evaluate a user view

**Status:** accepted — landed 2026-08-11 (see §Findings)
**Date:** 2026-08-11
**Milestone:** M0131 (S5)

## Problem

A real PG 18.3 hosted on a goopg catalog can *see* a goopg user view, reports
`relhasrules = true` for it, and reconstructs its defining SELECT with
`pg_get_viewdef` — but `SELECT * FROM <view>` fails 42809.

The rule text is not the problem. goopg writes a byte-correct `pg_rewrite`
`_RETURN` row with a canonical `pg_node_tree` `ev_action`
(`internal/executor/sys_pg_rewrite.go:90-107`), and `buildUserPGClassRow` stamps
`relhasrules = tbl.RuleIsCanonical` (`internal/executor/pg18_user_catalog_rows.go:570`),
so PG's relcache *does* enter the rule-loading path. The **index** is missing:
`RelationBuildRuleLock` finds the rule only through 2693, and goopg never
inserts a leaf there. `postgres/src/backend/utils/cache/relcache.c:785-806`:

```c
	ScanKeyInit(&key,
				Anum_pg_rewrite_ev_class,        /* = 3, pg_rewrite_d.h:29 */
				BTEqualStrategyNumber, F_OIDEQ,
				ObjectIdGetDatum(RelationGetRelid(relation)));
	...
	rewrite_scan = systable_beginscan(rewrite_desc,
									  RewriteRelRulenameIndexId,
									  true, NULL,
									  1, &key);
```

`indexOK = true` is a hard constant. `systable_beginscan`
(`postgres/src/backend/access/index/genam.c:397-401`) takes the heap-scan branch
**only** when `indexOK` is false, `IgnoreSystemIndexes` is set, or
`ReindexIsProcessingIndex(indexId)` is true — none applies to an ordinary
backend, so an empty 2693 yields `rd_rules == NULL`.

The failure is then **silent, not a crash**. `RelationBuildDesc` gates on the
flag (`relcache.c:1249-1255`: `if (relation->rd_rel->relhasrules)
RelationBuildRuleLock(relation); else { rd_rules = NULL; rd_rulescxt = NULL; }`)
and the invalidation self-heal at `:4313-4318` retries once, then quietly clears
the local `relhasrules` copy. A view with no rule keeps `rd_tableam == NULL`, so
the planner raises at `postgres/src/backend/optimizer/util/plancat.c:139-147`,
whose comment describes this exact situation:

```c
	/*
	 * Relations without a table AM can be used in a query only if they are of
	 * special-cased relkinds.  This check prevents us from crashing later if,
	 * for example, a view's ON SELECT rule has gone missing. …
	 */
	if (!relation->rd_tableam) { … ERRCODE_WRONG_OBJECT_TYPE …
	                             errdetail_relkind_not_supported(…) }
```

**Why `pg_get_viewdef` still works.** `ruleutils.c` reaches `pg_rewrite` through
SPI, not the relcache: `query_getviewrule` is `"SELECT * FROM
pg_catalog.pg_rewrite WHERE ev_class = $1 AND rulename = $2"`
(`postgres/src/backend/utils/adt/ruleutils.c:336`), prepared at `:820`, executed
at `:834` — an ordinary planned query the planner seq-scans over a nine-row
heap. **The heap row is fine; only the index path is blind.** Hence
`TestE2E_FailoverGoopgToPG` asserts `pg_get_viewdef` today while its row-level
probe (`internal/testport/e2e_failover_goopg_to_pg_test.go:510-512`) is a soft
`t.Logf`.

**Ledger correction.** Rows #428/#995/#996 name index **2620**. 2620 is
`pg_trigger` (`postgres/src/include/catalog/pg_trigger.h:34`). The pg_rewrite
indexes are 2692/2693 on heap 2618 (`postgres/src/include/catalog/pg_rewrite.h:32`,
`:56-57`): `CATALOG(pg_rewrite,2618,RewriteRelationId)`;
`pg_rewrite_oid_index, 2692, … btree(oid oid_ops)`;
`pg_rewrite_rel_rulename_index, 2693, … btree(ev_class oid_ops, rulename name_ops)`.

## Design

### The in-tree precedent

Same bug AI-20260810-011258-003 fixed for `pg_attrdef`, same fix shape.
`writeAttrdefRow` (`internal/executor/sys_pg_attrdef.go:74-97`) captures the TID
`writeHeapRowCanonical` returns (`internal/executor/operators_ddl.go:14048`) and
feeds both declared btrees — `insertPgAttrdefAdrelidAdnumIndexEntry` (2656) then
`insertPgAttrdefOidIndexEntry` (2657) — commenting that `AttrDefaultFetch` reads
defaults index-only. `writeViewRewriteRow` instead **discards** it:

```go
	if _, err := writeHeapRowCanonical(ctx, rel, PGRewriteColumnsPG18(), row); err != nil {
		return err
	}
	return nil
```

### S5.1 — key encoder for an `(oid, name)` composite

`internal/executor/sys_catalog_index_insert.go` has `buildIndexTupleOidKey`
(`:66`, 16 B, one uint32 — reusable verbatim for 2692) and
`buildIndexTupleNameOidKey` (`:85`, name-then-oid, for
`pg_class_relname_nsp_index`). 2693 needs the **opposite** column order, so new
`buildIndexTupleOidNameKey` + `cmpKeyOidName` are required. Port the layout
proven at `internal/initdb/btree_index_bootstrap.go:1554-1597`
(`pgBuildIndexTupleOidNameKey`) — verified byte-for-byte: `[0..5]`
`ItemPointerData` (blockhi, blocklo, offset, LE uint16); `[6..7]` `t_info` with
size in the low 13 bits (`sysIndexSizeMask` 0x1FFF); `[8..11]` LE uint32
`ev_class`; `[12..75]` 64-byte zero-padded `NameData` (`rulename`); `[76..79]`
MAXALIGN pad, never compared. Total 80 = `MAXALIGN(8+4+64)`; `NameData`'s
typalign is `'c'`, so no inter-attribute pad. `cmpKeyOidName` compares the LE
uint32 first, then memcmp over the padded name — the C-locale ordering the
bootstrap sorter uses (`btree_index_bootstrap.go:1681-1687`).

### S5.2/S5.3 — insert both entries at write time

Add `insertPgRewriteOidIndexEntry` (2692, reusing `buildIndexTupleOidKey` +
`cmpKeyUint32`) and `insertPgRewriteRelRulenameIndexEntry` (2693), both routing
through `insertCanonicalSysBtreeLeaf` (`:385`) to inherit lazy-root allocation,
descent, split and rebuild fallback. `writeViewRewriteRow` then captures the TID
and calls both — 2693 first, it is the load-bearing one. The allocated rule OID
(`ctx.Catalog.AllocOID()`) is the 2692 key; `tbl.OID` + literal `"_RETURN"` the
2693 key.

### S5.4 — mirror both indexes to `base/5`

`mirrorTouchedCatalogsToPostgresDB`
(`internal/executor/sys_catalog_postgres_db_mirror.go:137-192`) lists
`pgRewriteRelOID // 2618` at `:172` but neither index. `writeViewRewriteRow`
writes to `base/<tableCatalogHeapDBOid(ctx)>` (`base/1` in the standby lane)
while an attached PG connects `dbname=postgres` and reads `base/5`. An
unmirrored index is an *empty* index there — **blocker #8 exactly** (the
`pg_index` 2678/2679 case documented at `:184-191`, and the pg_attrdef case at
`:171-181`). Both OIDs must join the list beside 2618.

### S5.5 — stale index entries after DROP (verify, do not assume)

`stampViewRewriteRows` (`internal/executor/sys_pg_rewrite.go:205-211`, called
from `deleteCatalogRowsForOID` at `internal/executor/operators_ddl.go:13173`)
stamps only `xmax`; it does not touch leaves. PG behaves the same —
`systable_getnext` fetches through the index TID and applies the snapshot, so
dead rows filter out. The narrow risk is a **re-created** view: the stale leaf
points at a dead TID whose line pointer a later prune may reuse. Probe a
DROP/CREATE/VACUUM cycle on the standby; ledger if it diverges. The `pg_rewrite`
TOAST pair (2838/2839) stays out of scope — user-view blobs inline.

### Stale comment to correct

`internal/executor/sys_pg_rewrite.go:12-16` still says *"pg_class.relhasrules
stays FALSE for user views … a SEPARATE, blocked track"*. M0123-S3 sub-slice 2c
superseded that (`pg18_user_catalog_rows.go:570`). Rewrite it to name the index
gap as the sole remaining blocker.

## Guards

1. `buildIndexTupleOidNameKey` unit test: the 80-byte layout, `[8..11]` oid,
   zero-padded `[12..75]` name, and byte-identity with
   `pgBuildIndexTupleOidNameKey` for the same inputs — a SIBLING pair that must
   not drift.
2. `cmpKeyOidName` unit test: oid dominates; equal oid orders by padded-name
   memcmp; a 64-byte-truncated name compares stably.
3. Component test: `CREATE VIEW` → `base/<db>/2692` and `base/<db>/2693` each
   hold one leaf whose `t_tid` resolves to the live `pg_rewrite` heap row.
4. Mirror test: after `CREATE VIEW`, `base/5/2692` and `base/5/2693` are
   byte-identical to their `base/1` counterparts.
5. **The gate.** In `internal/testport/e2e_failover_goopg_to_pg_test.go`, promote
   the soft probe at `:510-512` to a hard assertion after the existing
   `pg_get_viewdef` block (`:448-464`): on the promoted PG,
   `SELECT count(*) FROM public.b5c_view` must equal
   `SELECT count(*) FROM public.bench_log WHERE client > 0`. That statement
   fails 42809 today. Extend to `b5c_view2` (`:471`) and `b5c_view3` (`:491`)
   once green.
6. Re-attribute the stale explanation at `:505-509`, which blames the copied
   `pg_internal.init` — S10 proves `RelationCacheInitFileRemove()` deletes that
   file before any backend reads it.
7. DROP VIEW → recreate → `VACUUM` → re-query on the standby (the S5.5 probe).
8. `go test -run '^TestE2E_FailoverGoopgToPG$' ./internal/testport/` green.
9. UNITS + SMOKE green.

## Findings (implementation, 2026-08-11)

**The headline: a real PG 18.3 hosted on a goopg catalog now expands and reads
goopg-authored user views.** `SELECT count(*) FROM public.b5c_view` — 42809 for
the whole life of the M0123 line — returns the base-table row count on the
promoted standby, and so do `b5c_view2` (bool/null WHERE) and `b5c_view3`
(searched CASE WHERE). The design's diagnosis was exactly right: the canonical
`ev_action` serializer was never the blocker, the missing 2693 leaf was.

Three corrections to the plan above:

1. **S5.1 was already done.** `buildIndexTupleOidNameKey` and `cmpKeyOidName`
   already exist in `internal/executor/sys_pg_enum.go` (added for
   `pg_enum_typid_label_index` 3503) with the identical 80-byte layout, so no
   new key encoder was written — only a cross-reference comment in
   `sys_catalog_index_insert.go` pointing at them, and Guard 1/2's separate unit
   tests are unnecessary duplication of that file's existing coverage.
   `cmpKeyOidName` delegates its name half to `cmpKeyName`, which is the same
   `bytes.Compare` over the 64-byte NUL-padded NameData the design specified.
2. **`"_RETURN"` was a bare literal in two places** once the index entry needed
   it (heap row + 2693 key). Promoted to a named `viewRuleName` const in
   `sys_pg_rewrite.go` — the heap row and the leaf key cannot drift apart.
3. **`mirroredOIDs` was an inline slice**, unreachable from a test. Split into
   `mirroredCatalogOIDs()` so `TestPgRewriteIndexesAreMirroredToPostgresDB`
   asserts membership statically. Every omission from that list has historically
   been its own multi-loop blocker (#8 for 2678/2679, the pg_attrdef trio), so a
   cheap membership guard is worth more than its two lines.

Guard status: 1/2 subsumed by the existing `sys_pg_enum.go` coverage (see
correction 1); 3, 4 and 7 landed as
`internal/executor/sys_pg_rewrite_index_test.go`
(`TestWriteViewRewriteRowMaintainsPgRewriteIndexes`,
`TestPgRewriteIndexesAreMirroredToPostgresDB`) — guard 4 as a static membership
assertion rather than a byte-comparison of the two files, and guard 7's
DROP/recreate cycle at the component level: after `stampViewRewriteRows` + a
re-sync the leaf-root holds a stale leaf beside a fresh one and **exactly one
resolves to a live heap row**, which is what `RelationBuildRuleLock` needs.
Guard 5 (the gate) and 6 landed in
`internal/testport/e2e_failover_goopg_to_pg_test.go`, extended to all three
views at once instead of `b5c_view` first — they were all already canonical, so
there was nothing to stage. A non-vacuity check was added there: the base count
must be non-zero, or every comparison would be trivially true. Guards 8 and 9
green.

The residual — a hosted PG pruning the `pg_rewrite` page and reusing the dead
line pointer the stale leaf points at — is unreachable in goopg's own operation
and needs a pre-promotion DDL phase in the failover test to observe; it is on
the deferral ledger rather than fixed here.

## References

- `postgres/src/backend/utils/cache/relcache.c:752-806`, `:1249-1255`, `:4313-4318`
- `postgres/src/backend/access/index/genam.c:388-401`
- `postgres/src/backend/optimizer/util/plancat.c:139-147`
- `postgres/src/backend/utils/adt/ruleutils.c:336`, `:820`, `:834`
- `postgres/src/include/catalog/pg_rewrite.h:32`, `:56-57`;
  `pg_rewrite_d.h:29`; `postgres/src/include/catalog/pg_trigger.h:34`
- `internal/executor/sys_pg_rewrite.go:90-107`, `:205-211`;
  `internal/executor/sys_pg_attrdef.go:74-97`
- `internal/executor/sys_catalog_index_insert.go:41-47`, `:66-107`, `:133-146`, `:385-390`
- `internal/executor/sys_catalog_postgres_db_mirror.go:137-192`
- `internal/initdb/btree_index_bootstrap.go:1554-1597`
- `docs/design/0131-bidirectional-cluster-dir-coldstart-and-system-views.md` §S5
