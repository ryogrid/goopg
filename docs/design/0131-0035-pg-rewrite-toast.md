# 0131-0035 — `DECLARE_TOAST(pg_rewrite, 2838, 2839)`: out-of-line `ev_action`

**Milestone:** M0131 (bidirectional cluster-directory cold-start + real-PG
system-view hosting) · **Slice:** S20 · **Status:** S20.1 landed 2026-08-12,
S20.2 open

## Why this exists

M0131-S9 grew goopg's on-disk system-view corpus from 6 to **71 of upstream's
80 `pg_catalog` views**. S9.3d then closed the remaining list: exactly **nine**
views are missing, and **eight** of them are blocked by a single absent
mechanism — pg_rewrite's TOAST relation.

goopg seeds each view's `pg_rewrite.ev_action` as an INLINE varlena,
pglz-compressed when it pays (`internal/initdb/pglz.go`,
`pglzVarlenaDatum`). That is a legitimate PG representation and it is what
upstream itself uses for small rules. It stops working at
`MaxHeapTupleSize = BLCKSZ - MAXALIGN(SizeOfPageHeaderData + sizeof(ItemIdData))`
≈ **8160 B** (`postgres/src/include/access/htup_details.h:615`). Eight captures
exceed it:

| view | `pg_column_size(ev_action)` |
|---|---|
| `pg_seclabels` | 35379 B |
| `pg_statio_all_tables` | 10475 B |
| `pg_stats` | 9316 B |
| `pg_indexes` | 9002 B |
| `pg_stats_ext` | 12196 B |
| `pg_stats_ext_exprs` | 11481 B |
| `pg_statio_sys_tables`, `pg_statio_user_tables` | dependents of `pg_statio_all_tables` |

Upstream stores all of them out of line:

```c
/* postgres/src/include/catalog/pg_rewrite.h:54 */
DECLARE_TOAST(pg_rewrite, 2838, 2839);
```

goopg bootstrapped **neither** relation, so a `varatt_external` pointer written
into `ev_action` would have named a relation that does not exist. That is the
whole of ceiling #1 in `0131-0009-system-view-corpus-widening.md`.

## Oracle measurement

Every value below was read from a freshly `initdb`'d **PostgreSQL 18.3** cluster
(`postgres/local_install/bin`), not inferred from headers.

```
pg_class 2838  relname=pg_toast_2618        relnamespace=99  reltype=0
               relam=2 (heap)  relfilenode=2838  reltoastrelid=0
               relhasindex=t  relpersistence=p  relkind=t  relnatts=3
pg_class 2839  relname=pg_toast_2618_index  relnamespace=99  reltype=0
               relam=403 (btree)  relfilenode=2839  relhasindex=f  relkind=i
               relnatts=2
pg_class 2618  reltoastrelid=2838
pg_attribute   2838 → chunk_id oid(26) len 4, chunk_seq int4(23) len 4,
                      chunk_data bytea(17) len -1; attnotnull=f on all three,
                      attstorage='p' on all three
               2839 → chunk_id, chunk_seq
pg_index 2839  indrelid=2838 indnatts=2 indnkeyatts=2 indisunique=t
               indisprimary=t indkey="1 2" indclass="1981 1978"
               (oid_ops, int4_ops) indcollation="0 0" indoption="0 0"
pg_type        NO row for either OID (reltype 0)
pg_depend      NO rows for either OID
chunk layout   TOAST_MAX_CHUNK_SIZE = 1996; every non-final chunk is exactly
               1996 B and sum(length(chunk_data)) == pg_column_size(ev_action),
               i.e. the value is pglz-compressed FIRST and the compressed bytes
               are what get chunked
```

A second measurement worth recording: on that same oracle **79 of the ~160
`pg_rewrite` rows are toasted**, including many views goopg already hosts
inline (`pg_roles`, `pg_shadow`, `pg_tables`, `pg_stat_activity`, …). goopg's
inline-compressed representation of those is not a divergence PG can observe —
`pg_column_size` differs, the datum does not. Only the eight above are *forced*
out of line.

## S20.1 — the relation pair (landed)

`internal/initdb/pg_rewrite_toast_bootstrap.go` introduces the declaration as
data, not as scattered constants:

```go
type toastRelPair struct{ Parent, ToastRel, ToastIdx uint32; RelName string }
func nailedToastPairs() []toastRelPair   // {2618, 2838, 2839, "pg_toast_2618"}
func nailedToastRels()  []nailedRel      // the two pg_class/pg_attribute rows
```

Wiring, and the reason for each choice:

| site | change | why |
|---|---|---|
| `bootstrapPgClassTuples` | append `nailedToastRels()` | the pair needs pg_class rows |
| `bootstrapPgAttributeTuples` | append `nailedToastRels()` | 3 + 2 column rows |
| `bootstrapPgClassRelnameNspIndex` | append + `pgClassRelnamespaceFor` | the index key IS (relname, relnamespace); a hardcoded 11 would leave `pg_toast.pg_toast_2618` unresolvable by name |
| `pgIndexInitialEntries` | new `entry(2839, 2838, …)` | detoast scans the index, never the heap |
| `pgClassRow` | namespace 99, `relkind 't' → relam 2`, `reltoastrelid`, `relhasindex`, reltype-0 exemption | the oracle row above |
| `pgAttributeRow` | `pgAttrStorageChar` | TOAST columns are `'p'`; the bytea default would be `'x'` |
| `Init` | `bootstrapToastRelationFiles` | one initialised empty heap page + one btree metapage in `base/{1,5}` |

**The pair is deliberately NOT a member of `nailedLocalRels`.** That list also
drives `bootstrapPgTypeTuples` and `writeRelcacheInitFile`, and a TOAST relation
belongs in neither: it has no `pg_type` row (verified above — `reltype = 0`, and
a defaulted `reltype = OID` would name a row nothing writes, tripping PG's
`rd_att->tdtypeid == relp->reltype` assertion in `relcache.c:4293`), and PG never
opens a TOAST relation during the critical relcache phase that consumes
`pg_internal.init` — it reaches one only while detoasting, long after.

goopg's own catalog reload is unaffected by construction:
`loadUserTablesFromHeapForDB` admits only `relkind ∈ {r,m,v,S}` with
`OID >= FirstUserOID`, so 2838 (`'t'`) and 2839 are excluded twice over.

### Acceptance

`assertHostedPGSeesPgRewriteToastRelation` in
`internal/testport/e2e_pg_coldstart_on_goopgdata_test.go` runs three escalating
reads against a **real PG cold-started on a goopg `$PGDATA`**: the parent's
`reltoastrelid`, both pair rows resolved by name through
`pg_class_relname_nsp_index`, and a `count(*)` that opens the TOAST heap and
reads block 0. Non-vacuity was verified by emptying `nailedToastPairs()`: all
three fail, the last with `relation "pg_toast.pg_toast_2618" does not exist`.

Three unit guards in `internal/initdb/pg_rewrite_toast_bootstrap_test.go` assert
the on-disk bytes at the fixed `FormData_pg_class` / `FormData_pg_attribute`
offsets a hosted PG casts — layout, not just Go-side values.

The TOAST heap is **empty** at S20.1, which is exactly the state upstream's own
initdb leaves it in before the first oversize datum is inserted.

## S20.2 — the chunk writer and the eight views (open)

1. Extend `pglzVarlenaDatum` (or a sibling) with an out-of-line branch: when the
   compressed payload would push the `pg_rewrite` tuple past the inline budget,
   split it into 1996-byte chunks, write them into `base/{1,5}/2838` with a
   fresh `chunk_id` OID, bulk-load `2839` over `(chunk_id, chunk_seq)`, and
   store an 18-byte `varatt_external` pointer
   (`va_rawsize`, `va_extsize`, `va_valueid`, `va_toastrelid = 2838`) in the
   column. `pg_seclabels` (35379 B → 18 chunks) makes multi-chunk mandatory,
   and 18 chunks × 1996 B still fits one 8 KiB page only 4 at a time, so the
   heap writer must be multi-page from the start.
2. **Sibling path (mandatory, same slice):** goopg's own reload
   (`loadViewsFromHeap` / `catalog_heap_reload.go`) must detoast an external
   `ev_action` pointer, or goopg will fail to reload the very rules it wrote.
   The reassembly primitive already exists for user data — reuse it, do not
   fork a second one.
3. Capture the eight views with `scripts/capture-ev-action.sh` and relax its
   guard #5 (the 8000 B inline budget) into "inline OR toastable" rather than
   deleting it — the budget still names the boundary at which representation
   changes.
4. Invert `assertNonCorpusSystemViewIsStillAbsent` (its subject `pg_indexes` is
   the first of the eight) and re-point it at the ninth view, `pg_policies`,
   whose blocker is unrelated: `pg_policy` (3256) is not an on-disk relation.

## Scope limits (ledgered)

- **initdb-time only.** Every existing goopg `$PGDATA` — the bench clusters
  65433/65436/65437 and any operator directory — keeps `reltoastrelid = 0` and
  has no `2838`/`2839` files. There is no in-place upgrade path; re-`initdb` is
  required. Same limit as M0131-S6/S12.
- **No negative-attnum rows.** Upstream writes six system-attribute rows
  (`ctid`, `xmin`, …) per table into `pg_attribute`; goopg writes user columns
  only, for every catalog. The TOAST pair follows goopg's existing convention
  rather than introducing a second one in one relation.
- **`relhasindex` stays false on every other nailed heap.** It is flipped to
  true only on the TOAST heap, where the `(chunk_id, chunk_seq)` btree is the
  only access path PG has to a chunk. Auditing the rest is its own slice.
- **`relfrozenxid`** is goopg's uniform 3, not the oracle's 744 — a
  pre-existing, corpus-wide choice, untouched here.

## References

- `postgres/src/include/catalog/pg_rewrite.h:54` — the declaration
- `postgres/src/backend/access/common/toast_internals.c` — chunking and the
  `(chunk_id, chunk_seq)` index scan every detoast walks
- `postgres/src/include/access/heaptoast.h` — `TOAST_MAX_CHUNK_SIZE`
- `docs/design/0131-0009-system-view-corpus-widening.md` — ceiling #1, the
  corpus census, findings F6/F7/F15/F19
- `docs/design/0131-0007-ev-action-capture-tooling.md` — the capture tool and
  its guard #5 inline budget
