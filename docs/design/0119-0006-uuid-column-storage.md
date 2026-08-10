# M0119-0006 — the `uuid` column stops being text

**Status:** landed 2026-08-10 (19th slice of M0119-0006)
**Area:** executor codec (heap storage), `internal/wal` pgoutput, PG-format
B-tree index key descriptor
**Siblings:** [`0119-0006-interval-column-storage.md`](0119-0006-interval-column-storage.md)
(the same shape, one type earlier)

## The defect

A `uuid` column was stored through `encodeValuePG`'s varlena default arm, i.e.
as the 36-character canonical text plus a varlena header — 37 bytes on disk.
goopg's **own** `pg_attribute` row for that column has always said something
else: `userTypeAttrsForOID(OIDUUID)` returns `{TypLen: 16, TypAlign: 'c',
TypStorage: 'p'}`, straight from the pg_type OID 2950 seed
(`internal/initdb/pg_type_seed_data.go:133`), which is upstream's
`struct pg_uuid_t { unsigned char data[UUID_LEN]; }`
(`postgres/src/include/utils/uuid.h`, `UUID_LEN` 16).

So the heap disagreed with the catalog by 21 bytes *and* by the presence of a
header. Consequences, all outside goopg:

- **A PG standby misreads the column.** It deforms the tuple from the
  TupleDesc: 16 raw bytes at the column's offset, which is the first 16
  characters of the text (`a0eebc99-9c0b-4e` as a uuid), and then every
  following column is 21 bytes out of position.
- **`HEAP_HASVARWIDTH` / `attcacheoff`.** `pgPhysicalTypeIsVarlena` reported
  `true` for uuid, so goopg *also* set the varlena infomask bit for a column
  PG's TupleDesc calls fixed — the mismatch PG18's `nocachegetattr` fast path
  (`heaptuple.c:642`, `Assert(j > attnum)`) exists to trip on.
- **PG-format index tuples were refused.** `pgIndexKeyImageIsPGFaithful`
  listed uuid alongside numeric precisely because of this: an attlen-16
  descriptor over a 37-byte varlena image would have handed `PGCompareUUID`
  the first 15 characters of the UUID's *text*, so uuid indexes were held on
  the pre-S11.4 blob path.

## Why nothing inside goopg was wrong

Unlike the interval slice — where text storage produced three wrong ANSWERS —
no goopg-visible result moved here. `uuid_cmp` is a `memcmp` over the 16 bytes,
and comparing the canonical lowercase-hex rendering of those bytes as text
yields the same order (hex digits `0`–`9` sort below `a`–`f` in ASCII, and the
hyphens sit at fixed positions in every value). That is exactly why the defect
survived: it is invisible from inside the engine and only a reader that trusts
the descriptor — a PG standby, or pg_amcheck's heap tier, which is what
M0119-0006 is about — can see it.

## What landed

The Datum representation is deliberately **unchanged**: uuid stays the
canonical `KindString` that index keys, comparisons, `Format()`, the analyzer's
`isTextBacked`, and the array-element encoder already speak. Only the on-disk
image moves.

| seam | file | change |
|---|---|---|
| encode | `internal/executor/codec.go` `encodeValuePG` | validate + normalize as before, then pack to 16 raw bytes via new `uuidBytesFromCanonical` (port of `string_to_uuid`) |
| decode | `internal/executor/codec.go` `decodePhysicalPGValueMctx` | read 16 bytes, render with new `uuidCanonicalFromBytes` (port of `uuid_out`) |
| alignment | `physicalPGTypeAlign` | uuid → 1 (typalign `'c'`), was the default 4 |
| varlena verdict | `pgPhysicalTypeIsVarlena` | uuid → false, was the varlena default |
| logical replication | `internal/wal/pgoutput.go` | `pgoPhysicalAlign` 'c' arm + a `case "uuid"` in `pgoDecodePhysicalValue` — the SECOND decoder of the heap layout, which would otherwise read the first raw byte as a varlena header |
| index descriptor | `internal/executor/pgindex_keydesc.go` | uuid dropped from `pgIndexKeyImageIsPGFaithful`, so a uuid index now gets the PG-format tuple key path with `btree.PGCompareUUID` |

The last row is an unlock that came free: the guard that refused uuid was
written to FAIL the moment `encodeValuePG` became PG-faithful
(`TestBuildPGIndexKeyDescRefusesNonPGImages`), and it did — the units gate
pointed at the exact follow-up. `numeric` is now the only type left on that
list.

## Gates

- `TestUUIDColumn*` (7, `internal/executor/codec_uuid_column_test.go`) — byte
  layout pinned directly (not only via round trip, which the old text form also
  passed), the two predicates pinned against the published `pg_attribute` row,
  all three `uuid_in` input spellings, invalid-input 22P02 retained, byte-order
  comparison unchanged, and a mixed-column tuple walk that catches a width slip
  in the following columns.
- `TestPgoDecodeUUIDMatchesPGNativeLayout`, `TestPgoPhysicalAlignUUID`
  (`internal/wal/pgoutput_uuid_test.go`) — the sibling decoder.
- `TestPGIndexTupleKeyOrdersEveryDescribableType/uuid` — uuid added to the
  per-type ordering table; its last pair differs only in the final byte, which
  the pre-flip 16-byte window onto the text could not have seen.
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS,
  `scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35), `TestPort_RegressSuite`
  PASS (hard-won rule #5, mandatory after a codec change), pre-commit pgbench
  smoke PASS.

## Deferred (see `.ralph/deferral_ledger.md`)

- `uuid[]` elements are still text inside the ArrayType blob —
  `encodeArrayValuePG` is a separate path, the same residue the interval slice
  left.
- No on-disk migration: a cluster written by an older goopg has 37-byte uuid
  images that the new decoder reads as a truncated uuid. goopg has no
  catversion-gated upgrade path for user heaps at all; this is the general
  gap, recorded once rather than per type.
- `numeric` remains the last heap-side divergence of this class (decimal
  string, not base-10000 `NumericData`) — already carried by the M0130-S11.4
  B2-a row.
