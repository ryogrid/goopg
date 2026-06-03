# Practice card — codec / storage / on-disk format change

**Load when** the task touches `internal/access/`, `internal/storage/`, row
encode/decode, page format, Datum layout, or catalog tuple format.

**Why:** a codec change regressed **6 regress tests at once**
(`m0106_codec_regressed_6_regress_tests`) because encode and decode drifted out
of agreement. On-disk format changes also silently invalidate existing data
dirs.

## Agreement checklist (encode ↔ decode must match on ALL of)

1. **Type set** — both sides handle the same set of types.
2. **Datum-Kind** — cross-Kind equivalences (e.g. `String` ↔ `StringArena`) must
   be honored in every comparison site (`m0073_arena_q5_heap_drop` found 5+).
3. **Fixed-width normalization** — e.g. `name` clipped to 63 bytes; both sides
   must normalize identically.
4. **Header-driven decode** — decode from the row header, not a bare
   `DecodeRow` assumption (`analyze_stats_target_test_failing_at_head`).

## Must-run gate

- **Re-run the FULL regress suite** after any codec change — not just the
  package's tests. Codec drift surfaces in unrelated tests.
- If the on-disk format changed, **re-init the data dir** before testing; old
  data is unreadable and will produce misleading failures
  (`pg_lsn_completed` notes a full TPC-H re-run was needed after an S2 format
  change).

## Trap

A unit test on the encode path can pass while decode is wrong (and vice-versa) —
this is the `pattern_sibling_paths_must_agree` failure mode. Test the round-trip,
not one direction.
