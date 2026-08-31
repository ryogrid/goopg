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

## S20.2b — the captures (landed 2026-08-12)

Guard #5 in `scripts/capture-ev-action.sh` is now **"inline OR toastable"**.
`MAX_EV_ACTION_STORED` still names the same 8000 B boundary as
`maxInlineEvActionStored` — what changed is that crossing it is no longer
fatal. An over-budget capture is admitted only when the tree still carries the
out-of-line path (both `{Parent: 2618, ToastRel: 2838, …}` and
`externalizeVarlenaPayload` are grepped for out of the Go tree, exactly as the
pins and the pg_type set already were) **and** the oracle itself stores that
value out of line under `chunk_id = rule_oid + 1` with the chunk lengths
summing to `pg_column_size`. That second half re-measures F20 and F22 per
captured view instead of trusting the S20.2a run, and both halves are proven
fail-when-broken by scripted revert.

**The corpus goes 71 → 77 views.** Six captures entered: `pg_indexes` (12043),
`pg_stats` (12053), `pg_stats_ext` (12058), and the `pg_statio_*_tables` triple
(12174 out of line, its two dependents 12179/12183 inline at 1756/1759 B — F14
again, and the first place the corpus mixes an external base with inline
dependents across a view-on-view edge).

### F23 — `HEAP_HASVARWIDTH` was missing on every chunk tuple

The first hosted-PG run did not fail; it **crashed**, with
`TRAP: failed Assert("j > attnum")` at `heaptuple.c:642` inside
`nocachegetattr` ← `heap_fetch_toast_slice`. `initdb.hasVarWidthCol` decides
the infomask bit from a hardcoded list of type NAMES and **`bytea` was not in
it** — so every chunk tuple S20.2a wrote claimed to have no var-width column.
PG's `nocachegetattr` guards its entire var-width scan behind
`HeapTupleHasVarWidth` (heaptuple.c:588), so it took the fixed-width fast path
for attribute 3, ran off the end of the 8-byte prefix and asserted. An
assert-disabled build would have read a garbage offset instead of crashing,
which is the more alarming half.

This is the S20.2a inertness fee coming due: the writer could not be wrong in a
way anything noticed until a real value existed. The one-word fix is in
`hasVarWidthCol`; that the list is name-based and still incomplete (json,
jsonb, numeric, …) is ledgered.

### F24 — goopg's pglz is *better* than upstream's, so the heaps differ

Every externalised value stores 3-4 % smaller than upstream's:

| view | upstream | goopg |
|---|---|---|
| `pg_indexes` | 5 chunks / 9002 B | 5 / 8674 |
| `pg_stats` | 5 / 9316 | 5 / 8985 |
| `pg_stats_ext` | 7 / 12196 | **6** / 11743 |
| `pg_statio_all_tables` | 6 / 10475 | 6 / 10125 |

goopg's pglz encoder finds shorter output than PG's for the same input, and for
`pg_stats_ext` that costs a whole chunk. The DETOASTED value is byte-identical
— proven end to end by the hosted PG evaluating all six views — so this is a
divergence in the TOAST heap, not in the datum. Recorded rather than "fixed":
matching upstream's exact output would mean reproducing its match-search
heuristics, not its format. Ledgered.

### Two views stayed out, neither for a size reason

Both toast cleanly; both were measured against a hosted PG in this slice.

- **`pg_seclabels`** (12099, 35379 B, 18 chunks) — `could not open relation
  with OID 3596`. `pg_seclabel` (3596) and `pg_largeobject_metadata` (2995) are
  not on-disk relations in a goopg cluster. Same class as ceiling #4.
- **`pg_stats_ext_exprs`** (12063, 11481 B) — `type with OID 10029 does not
  exist`, then `Assert("OidIsValid(typentry->typrelid)")` (typcache.c:3082)
  during abort. goopg seeds the ARRAY type 10028 (`_pg_statistic`) and points
  its `typelem` at 10029, but never seeds a `pg_type` row for the composite
  rowtype itself. **Ceiling #6** — the first that is about a catalog's own
  rowtype rather than a missing relation or an unpopulated column.

`pg_stats_ext` also forced two more entries into the canonical pg_type table:
`_bool` (1000) and `_float8` (1022). 1022's `typalign` is `'d'`, not the `'i'`
every other array in that table carries — Catalog.pm:469 gives an array type
`'d'` when its element is, and float8 is.

### Acceptance

`assertNonCorpusSystemViewIsStillAbsent` is inverted onto `pg_policies`
(`pg_indexes` moved into the corpus), and
`assertHostedPGSeesPgRewriteToastRelation` now runs the read S20.2a could not:
`SELECT count(*) FROM pg_catalog.pg_indexes` on a real PG cold-started on a
goopg `$PGDATA`, which resolves the 18-byte pointer, scans
`pg_toast_2618_index` for chunk_id 12047, reassembles five chunks,
pglz-decompresses and `stringToNode`s the 70408-byte Query. It also pins the
whole heap as `chunk_id/chunks/bytes` triples, so a change to the split is a
test failure rather than a silent difference in what goopg writes.

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
