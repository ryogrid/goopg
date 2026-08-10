Task: M0130-S11.4 slice 3b-2b (catalog → key-descriptor mapper, ADDITIVE)
— DONE, committed, pushed. **3b-2c is next** and is now BIGGER than planned:
the writer flip moved into it (see the finding below).

Landed (NO on-disk change, no REINDEX — the mapper has no caller yet):
- `internal/executor/pgindex_keydesc.go`:
  `buildPGIndexKeyDesc(idx *catalog.Index) (*btree.PGIndexKeyDesc, error)`,
  plus `pgIndexComparatorForOID` (type OID → 3b-2a comparator),
  `pgIndexKeyTypeOID` (private built-in-spelling switch),
  `pgIndexCollationOrderable`.
- `pgindex_keydesc_test.go` (9 tests).

Key facts for the next loop (do not re-derive):
- **The finding that reshaped the plan:** the sibling-path rule is SYMMETRIC.
  A datum-writing writer against the surviving ~20 `CompareKeys` sites orders
  garbage just as badly as a descriptor reader against a blob writer — a real
  PG datum is not order-preserving under `bytes.Compare` for any type but
  bytea/text. So the writer flip CANNOT land without the comparison rerouting;
  both are 3b-2c now (fix_plan + design doc + ledger all updated).
- The mapper ERRORS (never nil `Compare`) on: non-btree AM, expression key,
  explicit opclass, non-bytewise collation, array/enum/user type, unknown type.
  Nil `Compare` means bytewise, correct only for today's encodings.
- Do NOT reuse `buildUserPGAttributeRow`'s type resolution — its `text`
  fallback for an unknown name would give an enum column the text comparator
  while goopg orders enums by sort order.
- `ColDescending`/`ColNullsFirst` are EMPTY for all-default ASC NULLS LAST →
  every read bounds-checked; the two bits are independent (`DESC NULLS LAST`).

Next step: M0130-S11.4 slice 3b-2c. Build the tuple-shaped comparison seam
FIRST — one `BTree`/bulk-builder method over whole tuples, because
`ComparePGIndexTuples` needs t_info's null bitmap and t_tid's natts/heap TID
that a bare key payload does not carry — then in the SAME commit flip
`encodeCompositeBTreeKey` (`operators_ddl.go:10781`),
`encodeIndexKeyFromCols` (`operators_storage.go:7148`) and `encodeArbiterKey`
(`operators_upsert.go:1490`) to per-column datums through
`btree.FormPGIndexTuple`, threading the descriptor through `btree.Options`
(`btree.go:1712`). REINDEX-required — say so in the commit message.
Re-read the fix_plan banner first (M-NIGHTLY filing is unconditional every
loop; all six `AI-20260810-*` items are already filed).

Gates run: `go build ./...` + `go vet ./internal/executor` clean;
`go test -run TestBuildPGIndexKeyDesc ./internal/executor` PASS;
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS;
commit-hook pgbench smoke PASS; `make ralph-state-guard` OK.
NOT run: `scripts/tpch-spotcheck.sh` and the TPC-DS SF0.5 gate — this slice is
additive dead code (no executor/planner/codec path changed), so Rule #1 does
not apply; 3b-2c DOES change the codec and must run both (the SF0.5 cluster's
indexes predate S11.2b — re-pin after a REINDEX).

In-flight: none.
