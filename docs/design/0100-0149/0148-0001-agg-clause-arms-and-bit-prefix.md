# M0148-0001 (partial) — aggregate clause arms + bit_in prefix scan

**Loop:** #15 (2026-11-01) · **Parent:** M0148-0001 `aggregates.sql` regress coverage closure
**Baseline:** `scripts/pg-regress-runner.sh -v aggregates` at `5a43057d6` → 40 hunks / 988 diff lines
**After this change:** 33 hunks / 891 lines (7 hunks closed; no new diffs)

## Why

M0148-0001's baseline classification put the two cheapest, highest-signal
gaps in the parser and the bit input scan:

1. **Aggregate clause cross-product cells.** PG's `gram.y:15747` parses every
   aggregate decoration through ONE rule —
   `func_expr: func_application within_group_clause filter_clause over_clause`,
   each clause optional. goopg's grammar inlines the cross-product as
   individual `name_or_call` alternatives (its established convention, see
   the `OVER` comment at pg_grammar.y), and four cells were missing:
   args-`ORDER BY` × `FILTER` (±`OVER`), `DISTINCT` args-`ORDER BY` × `FILTER`
   (±`OVER`), args-`ORDER BY` × `WITHIN GROUP`, `WITHIN GROUP` × `FILTER`.
   The FILTER machinery behind them (AST `FuncCall.Filter`, planner
   `buildAggregateCall` resolution + dedup key, executor `applyAgg`
   pre-transition filter hook) was **already complete** (M0097-0007/-0032/
   -0035) — only the grammar never reached it. That made
   `sum(two order by two) filter (where two > 1)` (the presort check),
   `aggfns(distinct a,b,c order by a,c using ~<~,b) filter (where a > 1)`,
   `percentile_disc(0.5) within group (order by thousand) filter (where
   hundred=1)` (aggordview1 → its CREATE failed, cascading into
   pg_get_viewdef + select + drop errors) hard 42601s, and
   `percentile_cont(p order by p) within group (order by x)` printed
   `syntax error at or near "within"` where PG raises its own
   `cannot use multiple ORDER BY clauses with WITHIN GROUP` (gram.y raises
   it in the production at `parser_errposition(@2)` = the WITHIN token).

2. **bit_in/varbit_in prefix arms.** `validateBitDigits` (codec.go) scanned
   only `0`/`1` from index 0. PG's `varbit.c:165-184` takes a leading `b`/`B`
   (binary), `x`/`X` (hex, 4 bits per digit, MSB-first), or binary from 0 —
   and the `B'…'` quoting is a SCANNER production (`scan.l` `xbstart` strips
   the quote), so text arriving at bit_in is the UNQUOTED `B0101`:
   `COPY bitwise_test FROM STDIN` row `1 1 1 1 1 B0101` (aggregates.sql
   bitwise fixture) died on the leading `B`, emptying the table and
   blanking the whole BIT_AND/BIT_OR/BIT_XOR row. The explicit
   `text::bit(n)` cast had no arm at all — it stringified, so
   `'B0101'::bit(4)` echoed the raw text and `'101'::bit(4)` accepted a
   3-bit value for bit(4).

## What landed

- `grammar/pg_grammar.y` `name_or_call`: nine new arms (the four cells plus
  the OVER pairs the grammar's cross-product convention requires). The
  args-ORDER-BY × WITHIN GROUP arm raises PG's exact production-time error
  (`Raw` message, default 42601, `Pos = $<p>8` = the WITHIN token, so the
  `LINE n:` caret lands under `within` exactly as the expected file pins).
  `make gen-parser`: conflict gate holds at exactly 60 S/R; goldens
  unchanged; `tokennums_gen.go`/`yacc_parser.go` regenerated.
- `internal/nodes/datum.go`: `ScanBitInput` — the faithful
  bit_in/varbit_in scan (prefix arms, rune-aware first-offending-char 22P02
  messages, hex nibble expansion, VARBITMAXLEN 54000 guard
  `varbit.h:83`); `parseBitFromString` (pgwire/pgnodes path) shares it.
  Typmod checks stay with the callers (bit_in 22026 exact / varbit_in
  22001 upper bound) because the BIT count is what they check — hex expands
  ×4, so only the scan knows it.
- `internal/executor/codec.go`: `validateBitDigits` → `scanBitInput`; the
  bit/varbit assignment arms store the CANONICAL digit text (prefix
  stripped, hex expanded) — `bit_out` prints bare digits, so the COPY'd
  `B0101` round-trips as `0101`.
- `internal/executor/expr.go`: string→bit/varbit cast arm (sibling of the
  M0134-0128 int→bit arm, at the `x.Typmod`-aware site) — cast-time bit_in
  scan + typmod check, PG's 22026/22001 at cast time.
- Tests: `internal/parser/agg_clause_arms_test.go` (AST shapes for every new
  cell + the production-raised error's message/Pos/Raw/Code),
  `internal/nodes/scan_bit_input_test.go` (prefix arms, quoted-form
  rejection — PG rejects the quoted text spelling too, hex, boundary
  constants), `internal/executor/bit_string_literal_test.go`
  `TestBitTextPrefixInsert` (cast round-trips, bit-count-vs-typmod error).

## Verification

- `scripts/pg-regress-runner.sh -v aggregates`: 40 → 33 hunks. Closed: the
  four FILTER syntax hunks (incl. the aggordview1 CREATE/SELECT/DROP chain —
  the view now exists and `select * from aggordview1` MATCHES the expected
  rows, proving `rank(...) WITHIN GROUP` + percentile_disc FILTER execute
  with the right values), the multiple-ORDER-BY wording hunk, the bitwise
  COPY + BIT_AND/OR/XOR row, and the presort-paggregation hunk.
- TPC-H spotcheck Q12=2/Q13=35 PASS; acceptance arm (off) 24 MATCH PASS;
  fireset-scope OUT (no optimizer/planner path files staged).
- Full `go test` green: internal/parser (goldens included), internal/nodes,
  internal/executor. Pre-commit smoke (full module minus cluster pkgs) PASS.
- postmaster full-suite residuals are PRE-EXISTING environment flakes,
  reproduced on the stashed clean HEAD under the same machine load:
  `TestExplainSplitAggregateChainCosts` (2-second absolute conn deadline
  covering ~1.2M in-process inserted rows; fails deterministically on the
  loaded host, passes idle — loop #14 and nightly CI are green),
  `TestSimpleQueryBatchAbortUndoesEarlierCreate{Table,Type}` and
  `TestConnTxSessionNilWhenNotExplicit` (instant under -parallel 4 load,
  pass isolated; CreateTable documented in loop #14).

## Residuals for M0148-0001 (filed as children in fix_plan)

Remaining 33 hunks: scalar-group-agg→InitPlan min/max index optimization;
GROUP BY FD reduction + pathkey canonical key order; Incremental Sort
presort use; parallel-aggregation selection (`balk`, `v_pagg_test`);
EXPLAIN `Output()` targetlist rendering (qualified Vars, `(InitPlan 1).col1`,
`PARTIAL` prefix, `Disabled:` lines); pg_get_viewdef deparser fidelity;
DROP-CASCADE dependency NOTICEs for views; DISTINCT-aggregate transition
call ordering.
