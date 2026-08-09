(idle — nothing in flight)

Last loop: M0119-0006 11th slice — ENUM expression index key encoding, made
type-directed. COMPLETE and committed.

The defect: `encodeArbiterExprKey` dispatches on the runtime Datum KIND, and an
expression key column has no catalog column, so the `KindString`→`KindEnum`
conversion every enum COLUMN path performs (M0097-0022) never ran. The raw label
was written with `EncodeVarchar`, so every enum expression index came out in
ALPHABETICAL label order instead of `enumsortorder` (declaration) order —
`enum_ops` compares by enumsortorder (`enum_cmp`, PG `utils/adt/enum.c`). Over
`ENUM ('sad','ok','happy')` a real build stored `686170707900`/`6f6b00`/
`73616400`, the exact reverse of the type's order. Latent second half: a datum
that DID arrive as `KindEnum` (seq-scan injects those) wrote 8 float bytes into
the same index as variable-width label bytes — the float slice's
non-interleaving-byte-space shape.

Fix mirrors the float arm: `encodeArbiterExprKey` gained a `*Context` first
param (all 3 call sites had one), `exprKeyEnumType` resolves
`planner.ExprResultType`'s name via `catalog.InMemory.LookupEnum`, and
`enumSortOrderForKey` maps either kind onto the sort order → `EncodeFloat8`.

Method note worth carrying: a ~40-line throwaway `zz_probe_*_test.go` that
builds the index and `RangeScan`s the physical tree, logging `len(key)`/hex, is
the fastest way to see an encoding defect — the wrong order was obvious in the
key bytes before any theory. Same trick works for the remaining key types.

Design: `docs/design/0119-0006-expression-index-enum-key.md` (+ README row
`0119-0006f`). 1 ledger row: the DECODE side still declines enums, so amcheck's
opclass comparator declines any index carrying an enum expression key.

Gates run: `TestEncodeArbiterExprKeyEnumIsTypeDirected` +
`TestExpressionIndexBuildEnumKey` PASS (both proven non-vacuous by disabling the
new arm); `go test ./internal/executor/ ./internal/access/btree/
./internal/planner/` PASS; units precommit PASS; `scripts/tpch-spotcheck.sh`
PASS (Q12 rows=2, Q13 rows=35); pgbench hook PASS at commit;
`make ralph-state-guard` OK (auto-repaired the stale completed marker).

NEXT LOOP (state, not authority — re-read the `## Current Priority` banner).
M0130 is all `[x]` and M-NIGHTLY has no open item. Remaining M0119-0006 work:
checkunique posting-list duplicates, `box`/`int4range`/`int4[]`/`interval` key
encodings (no encoder arm at all), unscoped whole-DB pg_amcheck, and the enum
DECODE seam from this loop's ledger row.

In-flight: none.
