# Binary `COPY` of the unsigned-identifier family (`oid`/`regproc`/`xid`/`xid8`) and `uuid`

- Milestone / task: **M0119-0006** (54th slice)
- Status: **accepted / implemented**
- Date: 2026-08-13
- Predecessors: `0119-0006-copy-binary-float.md` (53rd), `0119-0006-copy-binary-int2.md`
  (52nd), `0119-0006-copy-binary-time.md` (51st), `0119-0006-uuid-column-storage.md`

## Problem

`internal/executor/copy_binary.go` dispatches on the column's declared type name
and falls through to a default arm that ships "whatever the `Datum` happens to
hold" — `KindString` as its bytes, `KindBytes` as its bytes, `KindInt` as **eight**
big-endian bytes, anything else as `Datum.Format()`'s text. Five PG types with a
fixed non-8-byte send format had no arm at all:

| type | pg_type | upstream send | goopg at HEAD | verdict |
|---|---|---|---|---|
| `oid` | 26, typlen 4 | `oidsend` = `pq_sendint32` (`oid.c:71`) | 8 bytes (KindInt escape) | **wrong width** |
| `regproc` | 24, typlen 4 | `regprocsend` **is** `oidsend` (`regproc.c:208`) | 8 bytes | **wrong width** |
| `xid` | 28, typlen 4 | `xidsend` = `pq_sendint32` (`xid.c:67`) | 8 bytes | **wrong width** |
| `xid8` | 5069, typlen 8 | `xid8send` = `pq_sendint64` (`xid.c:225`) | 8 bytes | accidentally right |
| `uuid` | 2950, typlen 16 | `uuid_send` = `pq_sendbytes(…, 16)` (`uuid.c:192`) | 36 ASCII bytes | **wrong shape** |

A binary `COPY` field whose length does not match what the type's `recv` reads is
`"incorrect binary data format"` to a real client — `CopyReadBinaryAttribute`
runs `pq_getmsgend` after every attribute
(`postgres/src/backend/commands/copyfromparse.c`). So unlike the 53rd slice's
`+Infinity` case, none of these is *silent* corruption on the wire; they are
streams no PG client can read at all.

The decode half was the worse one. Every one of the five came back from the
default as `NewStringDatum(string(payload))`, so an `oid` loaded through binary
`COPY` was a **string Datum holding four raw bytes** — it did not compare, sort,
index or render like the same `oid` arriving through `INSERT`. That is Hard-won
Rule #2 (encode↔decode are twins) failing in the direction that produces wrong
answers rather than errors.

## What upstream actually does

All four of `oid`/`regproc`/`xid`/`xid8` are one family: unsigned, pass-by-value,
no varlena, printed with `%u` / `UINT64_FORMAT`. Their `recv` functions are
`pq_getmsgint(buf, sizeof(T))` / `pq_getmsgint64`; their `in` functions go through
`uint32in_subr` / `uint64in_subr` (`numutils.c`, reached from `oidin` at
`oid.c:41`), which raise **22003** outside the type's range rather than wrapping.
`regprocsend`/`regprocrecv` are literally `return oidsend(fcinfo);` with the
comment *"Exactly the same as oidsend, so share code"* — the same pairing goopg's
heap codec already uses.

`uuid` is a byte array (`struct pg_uuid_t { unsigned char data[16]; }`,
`postgres/src/include/utils/uuid.h`), so its send format is its on-disk image
verbatim — no endianness, no header.

## What landed

**Binary `COPY` arms, both directions** (`internal/executor/copy_binary.go`):
`oid`/`regproc` → 4 BE bytes, `xid` → 4 BE, `xid8` → 8 BE, `uuid` → the raw 16.
The decode twins rebuild the Datum shape the **heap** decoder produces for the
same value: `NewIntDatum` over the unsigned value for the four identifiers, and
the canonical lowercase-with-dashes `KindString` for `uuid` (the engine has no
uuid Kind — see `0119-0006-uuid-column-storage.md`). Each enforces its exact
length, mirroring `pq_getmsgend`.

**A shared coercion, not a fourth copy** (`pgUnsignedIDFromDatum`,
`internal/executor/codec.go`). This is the 53rd slice's `pgFloatFromDatum` move
repeated: the heap arms of `encodeValuePG` and the new COPY arms are twins
differing only in byte order, so they now share the Datum→uint64 coercion
*and* the range rule. `bits` selects 32 (oid/regproc/xid) or 64 (xid8) and the
type name selects the text in PG's own 22003 message. Sharing the check is what
makes "the heap and the wire agree about which values exist" structural rather
than aspirational — before this, the heap silently wrapped a negative or
over-4-billion value through `uint32(v)`.

**The finding: `xid8` was truncated to 32 bits on the heap.** `xid8` shared the
`xid` arm in `encodeValuePG` and was written as **four** little-endian bytes.
`pg_type` OID 5069 is `typlen 8`, `typbyval FLOAT8PASSBYVAL`, `typalign 'd'` —
and goopg's own `internal/initdb/pg_type_seed_data.go:190` has seeded `Len: 8`
all along, so the heap disagreed with the catalog it ships. Two consequences,
both measured: a `FullTransactionId` above 2^32 lost its high half (silent data
loss), and `physicalPGTypeAlign` had no `xid8` arm, so it fell through to the
default 4 — meaning a hosted PG deforming the tuple with its own descriptor
would find every column after an `xid8` at the wrong offset. Both halves fixed
here (8-byte LE encode + decode, `'d'` alignment), plus the **third twin**:
`internal/wal/pgoutput.go` had `xid8` on the same 4-byte arm and would have read
half a value and handed the next column a short offset.

`xid8` is the reason this slice found anything. Its COPY arm was accidentally
correct at HEAD, so writing the twin test — "the COPY payload and the heap image
must have the same width and the same value, differing only in byte order" — is
what pointed at the heap rather than at the wire. The same test shape found the
53rd slice's `float` spelling bug. It is now the standing drill for each arm.

## Scope boundary (deliberate)

The set is exactly the types the **heap codec already treats as fixed-width**.
`regclass`, `regtype`, `regrole`, `regcollation`, `regprocedure` and `cid` all
send as 4-byte identifiers upstream, but goopg's heap stores them as varlena
text; giving them a COPY arm alone would *create* the encode↔decode disagreement
this slice exists to remove. Ledgered, not silently skipped.

## Verification

- Seven fail-when-broken guards in `internal/executor/copy_binary_oid_test.go`,
  **all verified red at HEAD** by scripted revert: send shape/width, round-trip
  vs `decodePhysicalPGValueMctx`'s own Datum, heap-encode agreement (width +
  byte-swapped value), the `xid8` 8-byte/`'d'`-alignment pin with its `xid`
  4-byte/`'i'` sibling, the range check on both paths, the recv length checks,
  and `uuid`'s 22P02 gate incl. the no-hyphen spelling.
- Oracle E2E on a capped throwaway goopg server (5533) against PG 18.3 (65432),
  same DDL and same rows on both: `COPY … TO … (FORMAT binary)` produced
  **byte-identical** 171-byte files, and each engine's file loaded cleanly into
  the other with identical values back — including `xid8 = 9007199254740993`,
  which HEAD would have truncated to its low 32 bits.
- `internal/executor`, `internal/wal`, `internal/catalog`, `internal/initdb`,
  `internal/analyzer` PASS; `go build ./...` + `go vet` clean; UNITS scope PASS;
  `scripts/tpch-spotcheck.sh` PASS; pgbench smoke via the commit hook.

## Deferred

See `.ralph/deferral_ledger.md` (2026-08-13, M0119-0006): the `reg*`/`cid`
varlena-text storage gap above; `xid8` values above `math.MaxInt64` (goopg's
Datum is a signed `int64`, so the top half of the unsigned range is
unrepresentable — `18446744073709551615` is rejected on input where PG accepts
it); and the still-missing binary `COPY` arms `interval`, `jsonb` (leading
version byte) and `bpchar`.
