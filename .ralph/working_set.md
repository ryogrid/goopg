# Working set (loop #20 → next)

Task: M0146-0009b (impl, done this loop) — bpchar truelen semantics in
planner statistics comparisons; fixes the `char(20)` MCV-probe miss
M0146-0009a root-caused.

## Files touched
- `internal/optimizer/selectivity.go` — `statLiteralEqual` (bpchar-aware
  literal-vs-MCV compare) + `typeName` param on `eqSelectivityForColumn`,
  `histCmp`, `bucketFraction`; threaded at all callers
- `internal/optimizer/joinselectivity.go` — type plumbing into the two
  `eqSelectivityForColumn` call sites
- `internal/optimizer/cardinality.go` — `joinKeyTypeName` classifier;
  truelen pairing in `eqjoinselSemiCore`/`eqjoinselInnerMCV` when both
  keys are bpchar-family; `strings` import
- `internal/optimizer/pathbitmap.go`, `pathindexrestrict.go`,
  `pathparamindex.go` — `indexKeyEqSelectivity` gains column type via
  new `columnTypeByName` helper
- `internal/optimizer/patternsel.go` — type plumbing at the two
  `eqSelectivityForColumn` sites (LIKE arm keeps byte semantics, as PG)
- Tests: `selectivity_test.go` (`TestSelectivityEqualityBpcharTrimsPadding`),
  `joinselectivity_test.go`, `joinrelsizejointype_test.go` call-site
  updates; `exprwalk_inventory_test.go` registers
  `cardinality.go:joinKeyTypeName` as a non-recursive classifier
- `docs/design/0100-0149/m0146-0009b-bpchar-stats-compare.md` — new
- `docs/design/README.md` — index row
- `analysis/m0146/m0146-0009b/` — README + gate evidence (parity diffs,
  fireset results, EA captures)
- `.ralph/fix_plan.md` — 0009b [x] + filed 0009c (recon, 5 NEW EA
  findings) and 0009d (impl, ea-ratchet vacuous-PASS)
- `.ralph/deferral_ledger.md` — three M0146-0009c finding-class rows

## Key symbols
- `statLiteralEqual` (selectivity.go) — `strings.TrimRight(x, " ")`
  both sides when `typeName` is bpchar-family (`char`/`bpchar`/
  `character`); byte-equal otherwise. PG: `bpchartruelen`
  (postgres/src/backend/utils/adt/varchar.c:675), `bpchareq` (:743)
- `histCmp` / `bucketFraction` — same truelen arm for histogram bounds
- `eqjoinselSemiCore` / `eqjoinselInnerMCV` (cardinality.go) — MCV-MCV
  pairing normalizes when BOTH join keys are bpchar (PG resolves
  `bpchar = bpchar` to `bpchareq`)
- `columnTypeByName` (next to `columnStatsByName`) — catalog lookup for
  index-qual paths; `joinKeyTypeName` — top-node-only Expr accessor

## Findings / witness
- `cd_education_status = 'College'`: 9,604 → 272,433 (PG ~274k)
- Q7 Gather Merge: 2 → 45 (PG est 46, actual 1,904)
- Q27 Gather: 2 → 45 (actual ~1,957)
- SF0.25 plan parity: MATCH 12 → 15 (+Q12/Q20/Q98, zero losses);
  Q73 NL flip converged (divergence cats 7 → 4)
- SF1: zero plan changes. TPC-H floor match=6 (>= 3). Arm digest 24/24.

## EA ratchet — read before next loop
- First `make ea-ratchet` run hollow-passed: `:5534` held a FOREIGN
  postgres (pg_isready short-circuits `start_server`) and
  `tmp/c20a/data-sf025` was a stale empty clone → 99 `relation does not
  exist` ERRORs, `nodes scored: 0`, "PASS (52 fixed)".
- Real run on :5541 + fresh clone: 17 findings (was 52), both 0009b
  targets cleared, but **5 NEW**: Q85 (3 relsets, reason+wr+ws chain),
  Q78 (5-rel Merge Left Join est 1,407 vs 123,049), Q83 (4-rel Merge
  Join est 1 vs 22) — all `pg_est=null`, all on plans byte-identical
  baseline-vs-candidate ⇒ pre-existing drift unmasked, NOT a 0009b
  regression. Filed as M0146-0009c; harness fix as M0146-0009d.

## Next step
Per banner: M0146-0009c (recon — per-level estimate decomp of the three
finding classes, 0009a recipe) or M0146-0009d (impl — gate must verify
EA_PORT server identity and fail on scored==0 / all-ERROR captures).

## Gates run (all PASS on staged code_tree 80e74c96)
- `go test ./internal/optimizer` — green
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` — green
- `scripts/tpch-spotcheck.sh` — Q12=2, Q13=33
- `scripts/tpcds-sf025-regression.sh sweep` — 96 PASS / 0 mism / 0 err
- `scripts/tpch-acceptance-arm.sh` — 24/24 value-identical to baseline
- TPC-H parallel floor capture — match=6 (floor 3)
- `scripts/tpcds-fireset-gate.sh` — 25 SF0.25 fires PASS both arms,
  SF1 no changed plans
- `make ea-ratchet` — FAIL(5 NEW) on the real run, preserved + filed

## In-flight
- none (private EA/fireset lanes stopped; foreign :5534 left alone)
