# D-09 (MD-1x) — Conditional alignment, both directions

Status: accepted. Implements TODO_ALL.md D-09. Blocked on D-01 (done —
`attStorage` on `colTypeInfo`). Bundle design: `minimize_datum` 03 §3
(D1), §3.3, TD-4; gate 06 §3 MD-1x. Independent of D-03…D-08 (03 §7.2):
lands alone, never inside another item.

## 1. Objective

`att_align_datum` on encode, `att_align_pointer` on decode,
generalising `internal/catalog/codec.go:1693-1695` (the only conditional
alignment in the tree). **Changes the on-disk format** (column-data
padding only — headers, null bitmap, hoff unchanged).

## 2. Oracle

PG `heap_fill_tuple`/`fill_val`: three of five varlena arms skip
alignment (`attispackable && VARATT_CAN_MAKE_SHORT`); decode uses the
peeking `att_align_pointer` (`tupmacs.h:104-112`, quoted in 01 §3.1):
a non-zero byte is a 1-byte length word OR the first byte of an
already-aligned 4-byte word — either way, no alignment. **The peek
decides only whether to align, never which header form it is**
(REVIEW M-pg-1: inverting this corrupts tuples).

## 3. Mechanism (one commit)

- **Encode** (`encodeRowPGCtx`, single heap funnel): encode the value
  FIRST, then place: if the column is packable-varlena AND the encoded
  bytes carry a short header → no align; else unconditional align as
  today. Packable = `pgPhysicalTypeIsVarlena(t)` (existing kind gate)
  AND effective `attstorage != 'p'`. NOT vacuous: `typstorage`
  defaults to `'p'` (`pg_type.h:192`), and four `typlen=-1` rows omit
  it — `int2vector`, `oidvector`, `tsquery`, `gtsvector` (in-tree
  transcription agrees for 22/30/3615). The check stays. Short-header
  test reads the header FORM — `b0&1 == 1 && b0 != 0x01`
  (equivalently `total == len(buf)`), never a length check: the
  13 B TOAST blob (`0x01` first byte) must not read as short. TOAST
  arm stays FIRST with unconditional align-4. Boundary verified:
  max short `0xFF` (126 B payload) vs 4 B at 127 B payload mirrors
  `VARATT_SHORT_MAX=0x7F`; empty → `0x03` short on both sides.
- **attstorage source**: per-COLUMN, never per-type. `Column.Storage`
  (`catalog.go:266`, set by `ALTER … SET STORAGE`,
  `operators_ddl.go:10504`, flushed to pg_attribute heap) overrides
  the type default — PG's `fill_val` reads the column's
  `attispackable`, and a type-keyed cache would pack a
  PLAIN-overridden text column short+unaligned where PG writes
  long+aligned (readable cross-engine via peek, but not
  byte-identical). `encodeRowPGCtx` therefore gains
  `info []colTypeInfo` (nil = resolve inline per column via
  `colTypeDescriptor` + `Column.Storage` override — no global cache,
  so domain DDL (`RegisterDomain`/`DropDomain`) cannot go stale).
  The hot heap-write path threads operator-Open-resolved info;
  cold paths pass nil (correct, slightly slower; extended if
  profiled). No fifth transcription in any mode (D-01 REVIEW
  M-goopg-2).
- **Decode** (`decodeRowRangeInfo` + header-less
  `decodePhysicalPGRowIntoMctx`): for varlena-kind columns, PEEK —
  `data[off] != 0` → no align; else align over the column's align
  (mask over all four classes, matching `att_align_nominal`; only
  2/4/8 ever fire — no core varlena uses 'c'/'s'). Non-varlena:
  unconditional as today. Uses `info[i].align` when present (already
  does). OOB guard FIRST, preserving `decodeRowRangeInfo`'s
  exhausted-data→NULL leniency at the (possibly unaligned) offset and
  the header-less path's `off==len` edge.
- **Second heap-bytes walker**: `encodePgoTuplePhysical`
  (`access/transam/xlog/pgoutput.go:310-341`) walks raw heap bytes
  with unconditional align — every packed short desynchronises it
  post-D-09 (same bug class as `catalog/codec.go:1693`). Gains the
  peek in this commit. Second precedent cited:
  `catalog/codec.go:1520+` (`DecodePGStatisticPhysicalRow.readVarlena`
  — full peek: 0x01-TOAST guard, short→no-align, 4B→align, OOB-first);
  its ANALYZE-restart coverage joins the gate. `catalog/codec.go:1693`
  stays (different seam — catalog arrays).
- **Backward read** (old nominal-aligned tuples): pad bytes are zero →
  peek aligns → identical to today (01 §3.1). Upgrade direction works
  by construction — no re-init, no migration. DOWNGRADE does NOT hold
  (old nominal decoder skips onto payload at unaligned shorts):
  downgrade unsupported, mixed-version standby and pg_upgrade-style
  concerns explicitly out of scope.
- **Forward read** (PG-authored unaligned short varlena): non-zero
  first byte → no align → the case goopg cannot read today (03 §3.1b).
  The `0x00`-first-byte 4 B header case (e.g. 127-char string,
  `total=128 → LE 0x200`) aligns — safe on both sides.

## 4. Non-goals

TOAST-pointer byte shape (03 §4 D2, out of scope — goldens exclude
TOAST columns with the reason stated); spill payload (D-10);
`Datum`-level serializers (different seam); planner/costing (no
`hashsize`/width change — packed widths only shrink where pads were).

## 5. Gate (06 §3 MD-1x)

- Byte goldens vs live PG 18.3: table over REAL types — `text`
  ('i'), `polygon`/`tsrange` ('d') × both varlena header widths,
  goopg bytes == PG bytes (extends
  `canonical_tuple_bytes_test.go:39-60` pattern; TOAST excluded with
  reason). Placement-only (aligned, width may differ) for
  storage-'p' varlena (`tsquery` small value: short+aligned here vs
  long+aligned PG — D-09 fixes placement, not header width; widening
  scope to a width rule is a separate item).
- Backward: old-encoder tuple decodes under the new decoder.
- Forward: PG-authored unaligned short varlena decodes.
- Suites: executor + optimizer + `access/transam/xlog` (pgoutput
  walker) + catalog + initdb (offset-pinning tests:
  `pg_attribute_attalign_offset_test`, `pg_proc_bootstrap_test`;
  TOAST-chunk rows shift offsets) + units scope + spotcheck green;
  TPC-H values-diff + TPC-DS sweep (format change touches every read —
  R8 mandatory, not conditional). `packedtuple.go:212` (in-memory
  TupleDesc format, TD-4) correctly untouched — self-consistent,
  peek-decoder reads nominal data fine.
- Sibling check: `catalog/codec.go:1693` + `:1520+` stay (different
  seams); note the relation in a comment, do not unify.
