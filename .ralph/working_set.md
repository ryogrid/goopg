Task: M0130-S11.4 slice 3b-2c-ii-B2-c-i (the prefix upper bound) — DONE,
committed + pushed. Next is the flip itself (B2-c, REINDEX-required).

Landed (no on-disk change, `pgIndexTupleKeys` still false):
- `indexFormat.compareHigh(entry, hi)` in
  `internal/access/btree/pgkeycmp.go` + helper `truncatedPGIndexTuple`.
  `rangeScanPos`' TWO `hi` tests (posting + normal branch) use it.
- For `desc == nil` it IS `CompareKeys` byte for byte ⇒ blob behaviour
  provably unmoved.

Why (do not re-derive): a range scan's two bounds are ASYMMETRIC once keys
are tuples. A prefix search key is a pivot; `ComparePGIndexTuples` makes the
SHORTER operand minus infinity. Right for the LOW bound (descent lands on
the group's first member); wrong for the HIGH bound — `compare(entry,hi)>0`
already holds for that first member, so `WHERE a=?` on a 2-col index returns
ZERO rows. Blob format hid this by faking plus infinity with bytes
(`appendCompositeUpperPadding`, 64×0xFF); a tuple cannot use that. Upstream
never invents a maximal key either (`_bt_check_compare`, nbtutils.c).

Guard: `internal/access/btree/prefix_highbound_test.go` (4 tests).
Mutation-checked: reverting `rangeScanPos` to `compare` → 30-row group
becomes 0 rows.

Still open (ledger row appended this loop):
- `keyExceedsHighKey` still plain `compare` — correct ONLY until 3b-3 lands
  `_bt_keep_natts` suffix truncation; then it needs the search key's own
  keysz, like upstream `_bt_compare`.
- `(*BTree).Search` has no bound sense (prefix point-lookup not expressible).
- The six `appendCompositeUpperPadding` sites still emit 0xFF padding.

Next step: M0130-S11.4 slice 3b-2c-ii-B2-c — THE FLIP.
`encodeCompositeBTreeKey` (operators_ddl.go:10785) / `encodeIndexKeyFromCols`
(operators_storage.go:7148) / `encodeArbiterKey` (operators_upsert.go:1490)
→ `pgIndexTupleKey` under `ctx.pgIndexKeyDesc(idx)`; ~20
`encodeBTreeKeyForColumn` probe sites; the six `appendCompositeUpperPadding`
sites → prefix PIVOT; `pgIndexTupleKeys = true`; explicit dual-format
decision for indexes the resolver refuses. Consider decomposing further
(writer-side vs probe-side funnel first, still blob). Gates:
tpch-spotcheck + **TPC-DS SF0.5 gate (mandatory)**, re-pin after REINDEX.
Re-read the fix_plan banner first (M-NIGHTLY filing unconditional; the six
`AI-20260810-011258-*` items are filed and left unchecked per the banner).

Gates run: `go build ./...` + `go vet` clean; `go test` PASS for
./internal/access/btree ./internal/wal ./internal/storage ./internal/amcheck;
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS;
`scripts/tpch-spotcheck.sh` PASS (Q12 rows=2, Q13 rows=35); commit-hook
pgbench smoke PASS. NOT run: TPC-DS SF0.5 gate (no query-execution change —
blob path is byte-identical).

In-flight: none.
