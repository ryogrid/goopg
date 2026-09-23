# Index scans and NULL-keyed rows: the interim guard

Status: landed 2026-09-24 (`821eda191`). The real fix, storing NULL-keyed
index entries, is filed as its own task (fix_plan M-NIGHTLY, "store
NULL-keyed index entries").

## The defect

goopg's btree stores no entry whose key has a NULL column. The bulk build
(`collectBTreeEntries` → `indexBuildEntryKey`) and the runtime writers
(`indexRowKey` → `indexRowKeyValues`) skip such rows. This dates from the
byte-key format, which had no null bitmap. PostgreSQL stores them:
`index_form_tuple` writes a null bitmap, and nbtree orders NULLs per column
(NULLS LAST for ASC).

So any scan that relies on an index containing every row of the table
returns too few rows when a key column it does not bind holds NULLs.
Measured on the default pipeline, against the same statement with the
index hidden behind `a + 0`:

| shape | rows (goopg / correct) |
|---|---|
| `WHERE a = 10` on `(a, b)` (also index-only, `count(*)`) | 3 / 4 |
| `a IN (10, 11)`, `a BETWEEN 10 AND 11` | 6 / 8 |
| `a = 10 AND b IS NULL` | 0 / 1 |
| nested-loop probe `r.a = o.x` | 9 / 11 |
| GROUP BY over index order (nullable single-column index) | 50 / 51 groups |
| merge LEFT JOIN, index-ordered outer side | 2000 / 2002 |

## The guard

An index may serve a scan only when every key column past the prefix the
scan binds is either declared NOT NULL, or (where the producer knows the
query's quals) strictly restricted by a top-level conjunct: a comparison,
an IN list, or IS NOT NULL. A row with a NULL there fails that conjunct
anyway, so the missing entry cannot drop a qualifying row
(`indexUnboundKeysNullSafe`, `qualsRejectNull` in `pathindexrestrict.go`).

| producer | bound prefix | quals-aware |
|---|---|---|
| path-search restriction / index-only (`pathindexrestrict.go`, `pathindexonly.go`) | clause columns | no (NOT NULL only; landed earlier) |
| `findBTreeIndexForColumn` (rule-based equality, IN, range, min/max, conjunct absorption) | 1 | yes |
| `pickIndexCoveringLeadingPrefix` (nested-loop / parameterised probes) | bound join columns | no |
| base and parameterised bitmap paths | clause columns | no |
| ordered index paths, index-ordered GROUP BY, the seqscan-off ordered index-only promotion | 0 (full scan) | no |

A single-column index probed on its own column is always allowed, since the
column is bound.

## Cost

No TPC-H or TPC-DS plan changed (TPC-DS SF0.25 plan-shape changed=0,
categories identical; TPC-H acceptance arm values identical). The upstream
regress pass-required subset keeps its pass/fail status. Two plans over
nullable `tenk1` columns lose their index shape where PG keeps it:
- `btree_index`'s composite SAOP probe with a range on the second column.
  That range is a strict qual, but the SAOP producer does not pass its
  quals yet.
- `limit`'s GroupAggregate over an index-only scan.

Both come back with the storage fix.

## The real fix (filed)

Store NULL-keyed entries in the tuple-format index. That format already
has the null bitmap and per-column NULLS FIRST/LAST comparison
(`pgcompare.go`). Needed with it:
- Writers: the build, runtime insert and upsert keep the entry. Uniqueness
  checks keep skipping NULL keys (NULLS DISTINCT).
- The key-change fingerprint must tell NULL apart from "no entry".
- Scans: an open-ended range bound must stop at the first NULL in the
  bounded column, as `_bt_checkkeys` does.
- Index-only scans must decode NULL attributes.
- amcheck must stop expecting no NULL entries.

Indexes built before the fix lack the entries until REINDEX, and the guard
stays for the byte-key (descriptor-less) format.
