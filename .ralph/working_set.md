(idle — nothing in flight)

Last loop: M0119-0006 ninth slice — index-expression RESULT-TYPE resolution and
the expression arm of amcheck's opclass comparator.

- NEW `planner.ExprResultType` (`internal/planner/expr_result_type.go`):
  resolves a resolved index expression's static type from the PG18 catalog seed
  goopg already ships (`catalog.LookupProcForNode`→`ProcResultType` = pg_proc
  prorettype; `LookupOperatorForNode` = pg_operator oprresult). DECLINES rather
  than guessing — `catalog.TypeNameToOID` silently falls back to text(25) for
  enums/domains, so `exactTypeOID` rejects anything landing on text that does
  not spell "text". Left `inferExprType` alone (different question, text
  default). Pinned in `exprSwitchInventory` under a new `failClosedTypeResolver`
  role (census gate demands a pin + ledger row for a new hand-written switch).
- NEW `exprKeyDecodeType` (`operators_upsert.go`, next to
  `encodeArbiterExprKey`): SQL type → DECODE SURROGATE, because the encoder
  dispatches on Datum KIND — an int4 expression key is 8 bytes (EncodeInt8) vs
  4 for an int4 column, a date one is int64 micros vs int4 days. bool/bytea
  decode but are routine-unsafe (wrong Datum kind for a user comparator);
  float and enum decline outright.
- `btIndexOpClassComparator` (`operators_bt_index_check.go`) no longer returns
  nil for an index with any expression key column; new `exprKeyColumnType`
  bridges the two.

Committed as the 9th M0119-0006 slice. Design doc
`docs/design/0119-0006-expression-index-result-type.md` (+ README row
`0119-0006d`), 2 ledger rows, fix_plan updated.

Gates run: units precommit PASS (after fixing the inventory-census failure it
surfaced); `scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35); pgbench hook PASS.
New gates confirmed non-vacuous (forcing `exprKeyColumnType` to decline makes
both damaged expression indexes report clean).

NEXT LOOP (state, not authority — re-read the `## Current Priority` banner;
M0130 is fully [x], so the banner points at M0119). Remaining M0119-0006:
  1. float4/float8 + enum expression key encodings (the two kinds
     `exprKeyDecodeType` declines; ledger 2026-08-10).
  2. posting-list duplicate coverage in the `checkunique` tier.
  3. `box`/`int4range`/`int4[]`/interval key encodings.
  4. unscoped whole-database `pg_amcheck` run in the 005 port.
Gate note carried forward: the TPC-DS SF0.5 sweep (~1 h) has NOT been run for
the last nine M0119-0006 slices; worth one run before M0119-0006 closes.

In-flight: none.
