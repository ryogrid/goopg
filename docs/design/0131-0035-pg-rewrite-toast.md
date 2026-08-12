# 0131-0035 — `DECLARE_TOAST(pg_rewrite, 2838, 2839)`: out-of-line `ev_action`

**Milestone:** M0131 (bidirectional cluster-directory cold-start + real-PG
system-view hosting) · **Slice:** S20 · **Status:** S20.1 landed 2026-08-12,
S20.2a (the writer) landed 2026-08-12, S20.2b (the captures) open

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

## S20.2a — the chunk writer (landed 2026-08-12)

`internal/initdb/pg_rewrite_toast_writer.go` turns an oversize seeded
`ev_action` into chunks plus an 18-byte pointer; `bootstrapToastRelationFiles`
grew a `chunks` parameter and now writes the populated pair instead of the
empty one when there is anything to write. The path is live but **inert**: no
corpus view is oversize yet, so a real `goopg init` still produces exactly the
S20.1 bytes. S20.2b's captures are what light it up.

### Second oracle pass

The first pass (S20.1) measured the *relation*; this one measured the *values*,
on the same kind of throwaway PG 18.3:

| view | rule oid | `pg_column_size` | raw text | chunks |
|---|---|---|---|---|
| `pg_indexes` | 12046 | 9002 | 70408 | 5 |
| `pg_stats` | 12056 | 9316 | 92367 | 5 |
| `pg_statio_all_tables` | 12177 | 10475 | 72286 | 6 |
| `pg_stats_ext_exprs` | 12066 | 11481 | 92109 | 6 |
| `pg_stats_ext` | 12061 | 12196 | 73811 | 7 |
| `pg_seclabels` | 12102 | 35379 | 203378 | 18 |

Three findings, each of which the headers alone would have got wrong:

- **F20 — the chunked bytes are the compressed varlena minus its 4-byte
  header, i.e. they open with `va_tcinfo`.** `toast_save_datum` takes
  `data_p = VARDATA(dval)` for a compressed datum, and `VARDATA` of a 4B
  varlena skips only the header — so the tcinfo word rides along into chunk 0
  and `create_detoast_datum` reconstructs the value by prefixing a fresh 4-byte
  header to the concatenated chunks. Confirmed byte-wise on the oracle: chunk 0
  of `pg_indexes`' value is `08 13 01 00 | 00 28 7b 51` — tcinfo 70408 (the
  uncompressed length), then the pglz control byte and the literals `(`, `{`,
  `Q` of `({QUERY`. A writer that chunked the whole compressed varlena would be
  off by exactly four bytes and would decompress to garbage.
- **F21 — `va_rawsize` is the uncompressed length + `VARHDRSZ` in BOTH
  branches**, coming from the tcinfo's extsize field when compressed and from
  `VARSIZE` when not. So one expression covers both, and PG's own
  "is it compressed?" test (`VARATT_EXTERNAL_IS_COMPRESSED`: extsize <
  rawsize − 4) falls out for free — goopg stores no flag of its own.
- **F22 — `chunk_id == rule OID + 1`, universally**, because upstream assigns
  the value id with `GetNewOidWithIndex` during the very insert that consumed
  the rule's OID. Verified as an invariant rather than a coincidence: no
  distinct `chunk_id` among the oracle's 280 chunk rows fails to match some
  `pg_rewrite.oid + 1`. goopg pins chunk_ids the same way it pins rule OIDs, so
  its `pg_toast_2618` is OID-identical to upstream's, not merely well-formed.

A fourth measurement corrects this document's own table: **`pg_statio_sys_tables`
(1756 B) and `pg_statio_user_tables` (1759 B) are NOT oversize.** They were
listed among the eight as "dependents"; they are blocked only by their base view
`pg_statio_all_tables`, and once it is captured they enter the corpus inline.
Six values are forced out of line, not eight.

### The sibling path is a decode arm, not a detoast

The plan called goopg's own reload the mandatory sibling. Reading it settles the
shape: `loadViewsFromHeapForDB` discards every rule whose `ev_class` is below
`FirstUserOID`, so it never *consumes* a bootstrap `ev_action` — but it decodes
**every** `pg_rewrite` row before applying that filter. The old code path took
an 18-byte on-disk pointer to `decodePhysicalPGVarlena`, which rejects header
`0x01` with *"external varlena not supported"* — a startup failure on a
directory goopg itself wrote. The fix is a `pg_node_tree` decode arm (sibling of
the existing KindBytes encode passthrough) that consumes the pointer verbatim as
`KindBytes`. It is **not** a detoast: reassembly needs the TOAST heap and a
buffer pool, neither of which the decoder has, and nothing reads the value.
User rules are unaffected either way — they store re-parsable SQL text and are
never toasted (`pg_node_tree` is not in `executor.isToastableType`). Ledgered.

### Guards

`internal/initdb/pg_rewrite_toast_writer_test.go`, all four break directions
proven fail-when-broken by scripted revert:

| guard | break direction proven |
|---|---|
| `TestExternalizeVarlenaPayloadCompressedLayout` | keeping the 4-byte `va_header` in the chunks (F20) → *"chunk 0 opens with tcinfo rawsize 132082, want 200152"*; `va_rawsize` taken from the compressed size (F21) → *"va_rawsize 33020, want 200156"* |
| `TestExternalizeVarlenaPayloadIncompressibleLayout` | the plain branch must read as NOT compressed |
| `TestPgRewriteEvActionDatumSwitchesRepresentation` | the corpus stays inline; an oversize value becomes an 18-byte pointer whose `va_valueid` is the rule OID + 1 (F22) |
| `TestWriteToastChunkHeapAndIndexRoundTrip` | unsorted index leaves → *"chunk_seq 15 after 16 — leaves must be key-ordered"*; also asserts the heap actually spilled past block 0 and that base/1 and base/5 agree |
| `TestPgRewriteRowExternalPointerSurvivesTheCodec` | removing the decode arm → *"decode pg_node_tree as varlena: external varlena not supported"*, i.e. the exact startup failure |

The test payload is a *varying* synthetic node tree. An earlier version repeated
one block verbatim, compressed ~100:1, and never crossed the inline budget — the
guard passed while testing nothing. Real captures compress 6–10:1 and the
generator now matches that band.

## S20.2b — the captures (open)

Items 1 and 2 landed as S20.2a above. What remains:

1. Capture the eight views with `scripts/capture-ev-action.sh` and relax its
   guard #5 (the 8000 B inline budget) into "inline OR toastable" rather than
   deleting it — the budget still names the boundary at which representation
   changes, and `maxInlineEvActionStored` in
   `internal/initdb/pg_rewrite_toast_writer.go` must keep naming the same
   number, or a view could pass capture and then produce a page no reader can
   parse. Six of the eight are oversize (see the table above); the two
   `pg_statio_*_tables` dependents enter inline.
2. Invert `assertNonCorpusSystemViewIsStillAbsent` (its subject `pg_indexes` is
   the first of the eight) and re-point it at the ninth view, `pg_policies`,
   whose blocker is unrelated: `pg_policy` (3256) is not an on-disk relation.
3. **The acceptance that S20.2a could not run**: a real PG cold-started on a
   goopg `$PGDATA` must `SELECT * FROM pg_indexes` — i.e. resolve the pointer,
   scan `pg_toast_2618_index` for its `chunk_id`, reassemble, decompress and
   rewrite the query. Until a capture exists there is no oversize value on
   disk, so the writer's output is only verifiable against PG's documented
   struct and goopg's own reader; extend
   `assertHostedPGSeesPgRewriteToastRelation` with the read the moment the
   first capture lands.

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
