# Working set — inter-loop baton

Task: **M0143-0007b (banner item 9) — SLICES 1-3 LANDED.** Task stays `[ ]`;
slice 4 remains. Owner approval on record (2026-09-20).

## Banner

Unchanged. Item 9's M0143-0007b is still the first actionable task — the
loop-52 banner walk (items 4-8 exhausted or blocked) still holds.

## Two escalations STILL unanswered

1. **M0145-0018 NO-GO** (loop 51) — blocker is the COST MODEL.
2. **The loop-48 ordering question** — M0145-0003 is strictly the first `[ ]`
   in item 3. Still the banner's call.

## Slice 3

**Corrected my own design doc**: it claimed no pgoutput site calls
`PadBpchar`. It does (`pgoDecodePhysicalValue`, `xlog/pgoutput.go`), already
pinned by a test. The wrong claim came from grepping a package path that does
not exist (`internal/wal/`) instead of finding the emitter.

**Two more consumers assumed trimmed storage.** Stored `char(10)` = 'ab':

```
                 PG 18.3   goopg before   after
bit_length(c)         16             80      16
length(c::text)        2             10       2
```

Root: **`char(n) -> text` is `rtrim1`** (`pg_proc.dat` oid 401) — the cast
STRIPS padding — and `bit_length` has no bpchar overload so it resolves through
that cast. Both now apply the rule explicitly. `strings` regress diff 263 → 248.

**Why no existing test caught it**: literal forms (`'ab'::char(10)`) never
reach `coerceTextLikeDatum`, so their datum stayed trimmed and the old
agreement held. Only a STORED column diverged.

Also ledgered: slice 2 widened a pre-existing v0 limitation —
`pgoDecodePhysicalValue` fails loudly on an external TOAST pointer, and wide
bpchar values are now toasted. PG has no such limitation. It is its own
feature, not part of R23.

## THE RULE, now confirmed three times

When a storage convention changes, the sites that RE-PAD are safe — they route
through one idempotent helper. The dangerous ones are **CONSUMERS that read the
stored image**. All three misses were consumers, and each was invisible to a
different gate:

- `length()` — needed the upstream regress suite
- the TOAST decision — needed a byte-level measurement no value gate performs
- the bpchar→text cast / `bit_length` — needed a STORED-column witness,
  because every literal-expression test bypasses the storage path

## Next step

**Slice 4** — reload and re-measure `relpages` on `customer`/`item` against PG.
That is the only way K41's original gap is shown closed in NUMBER rather than
in mechanism. Note it only materialises for newly written data, so the measure
needs a fresh load, not the existing cluster.

## Traps carried forward

- A private PG oracle is cheap (`initdb` into /tmp on a 55xx port) — use it
  instead of reasoning about upstream behaviour. It settled all three misses.
- Literal-expression tests bypass the storage path; use a stored column.
- Port 5560 is held by a PEER's server; do not touch it.

## Gates run

units PASS; regress `char` PASS (172 lines) + `varchar` PASS, `strings`
263 → 248, `text` unchanged at 14 (pre-existing); tpch-spotcheck PASS
(Q12=2 Q13=33); TPC-DS SF0.25 PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0
TIMEOUT=0, plans 99/99 identical; TPC-H acceptance arm 24 MATCH.

## In-flight

none
