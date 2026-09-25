# Freeze WAL records use PG's padded `xlhp_freeze_plan` (M-NIGHTLY freeze-WAL)

Status: **LANDED 2026-09-25** (`0b37c784b`). This corrects the A7-freeze
entry in `IMPLEMENTATION-TODO.md` (A7 complete log), which described one
freeze plan of 11 bytes. Evidence: `analysis/m-nightly/freeze-wal-plan-padding/`.

## Defect

PG's `xlhp_freeze_plan` (`heapam_xlog.h`) is `xmax` (4), `t_infomask2` (2),
`t_infomask` (2), `frzflags` (1), then `ntuples` (2). Since `ntuples` is
2-byte aligned, the C compiler inserts one pad byte after `frzflags`:
`sizeof` is 12 and `ntuples` sits at offset 10. Every PG reader uses that
size:
- the emitter, `log_heap_prune_and_freeze`, registers
  `sizeof(xlhp_freeze_plan) * nplans`;
- redo and `heapdesc.c` cast the block data to the struct array.

goopg's `EncodeHeapFreezePG` (`pg_assembled_emit.go`) and
`decodeXLogHeapPrune` (`recovery.go`) both used 11 bytes. They agreed with
each other, so goopg-to-goopg replay worked. PG 18.3 `pg_waldump` described
a one-plan, three-offset record as `ntuples: 256`, and a real PG standby
would misapply every freeze plan.

## Change

- The encoder writes the pad byte, and `sizeOfXLHPFreezePlan` is 12.
- The decoder still reads the legacy 11-byte form, so WAL written by an
  older build replays. Every field of a PG-layout block-0 payload is a
  multiple of two bytes, so its length is always even. goopg's only legacy
  shape (one 11-byte plan, then 2-byte offsets) is always odd. An odd length
  therefore identifies the legacy form without ambiguity.

## Tests

- `TestEncodeHeapFreezePGPlanLayout`: block-0 bytes against the C layout.
- `TestDecodeXLogHeapPruneLegacyFreezePlan`: a legacy record still decodes.
- `TestPGWaldumpReadsFreezePlan` runs PG's own `pg_waldump` and asserts
  `ntuples: 3, offsets: [1, 3, 5]`; on the old encoder it fails with
  `ntuples: 256`.
- `findPGWaldump` now resolves `postgres/local_install` from the xlog
  package's depth. Before, both waldump tests silently skipped in the
  gates unless `PG_WALDUMP` was set.

## Verified

- Units: PASS.
- `tpch-spotcheck`: PASS.
- TPC-DS SF0.25 sweep: 96 PASS, 99 plans unchanged.
- `TestE2E_PGStandbyFullCycle`, `TestPort_Recovery001StreamRep`,
  `Recovery013CrashRestart` and `Recovery039EndOfWal`: PASS.

The existing gap stays as it was: goopg freezes by rewriting `xmin` to
`FrozenTransactionId` and writes a zero plan (`xmax` 0, infomask 0), rather
than PG's infomask-bit freeze. The `EncodeHeapFreezePG` comment records it.
