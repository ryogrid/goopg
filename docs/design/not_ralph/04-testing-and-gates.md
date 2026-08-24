# 04 — Testing & Gates

## 1. Test taxonomy for this rewrite

| layer | what it is | role here |
|---|---|---|
| Existing parser tests | 97 files / 541 funcs, black-box over `Parse()`/`ParseExpr()` | PRIMARY behavioral spec. Must stay green through every wave — unchanged except documented behavior deltas (see §2 ledger disposition (a)); never a silent edit |
| Differential AST harness | NEW: runs both parsers on the same input, dumps canonical AST form, compares | the flip gate per wave (§3) |
| regress-runner (`scripts/pg-regress-runner.sh`) | goopg vs upstream expected outputs across ~232 cases | SQL-surface parity; pass-rate must be ≥ baseline at every flip |
| oracle-diff (`scripts/pg-oracle-diff.sh`) | targeted goopg-vs-PG18.3 probes | spot checks for the wave's statements |
| units suite (`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`) | whole-module minus cluster-backed pkgs | pre-commit bar, every commit |
| pgbench smoke | hook-enforced CI-parity workload | automatic on commit; never bypassed |
| tpch-spotcheck / tpcds SF0.5 | canonical row counts / fast regression sweep | cutover (P7) and any wave that could plausibly affect plans (it shouldn't — same AST — but cheap insurance) |

## 2. Differential harness design

* Canonical dump: a deterministic, reflection-based tree printer over AST
  nodes (pos fields zeroed) — lives in `internal/sqlparser/difftest_test.go`
  plus an export shim if needed.
* Corpus: every `Parse("...")`/`ParseExpr("...")` literal in existing tests
  is mechanically extractable; harness replays them against both parsers.
  During P1..P6 only inputs routed to the new parser are compared at flip
  time; before flipping, mismatches drive porting work.
* Known-difference ledger: where legacy parser is WRONG vs upstream
  grammar-wise but tests encode legacy behavior, the item goes to
  `difftest_known_diffs.md` in this directory with disposition:
  (a) fix new parser to match upstream AND update the test (documented
  delta), or (b) keep legacy behavior via goopg_ext.y rule + note.
  Disposition requires citing upstream gram.y lines.

## 3. Gate matrix (per phase)

Performance gate: P0 records micro-benchmark baselines (ns/op + allocs/op
for `Parse` and `ParseExpr` over representative inputs — SELECT-heavy,
DDL-heavy, expression-heavy); every flip compares against baseline with the
repo's timing hygiene (constant server age where servers are involved).
Regression beyond 2x on any input class stops the flip for investigation.

| phase | differential | parser pkg tests | units | regress rate | perf | extra |
|---|---|---|---|---|---|---|
| P0 infra | n/a | green | green | n/a | baselines recorded | `make gen-parser` reproducible (zero diff) + conflict-gate fires on seeded conflict |
| P1 SELECT family | required to flip | green | green | ≥ baseline | ≤ baseline+2x | TPC-H smoke query set parses |
| P2 expressions | required | green | green | ≥ baseline | ≤ baseline+2x | ParseExpr flips; **plpgsql suite** run (its exprs feed through) |
| P3 DML writes | required | green | green | ≥ baseline | ≤ baseline+2x | oracle-diff probes for ON CONFLICT/RETURNING |
| P4/P5 DDL waves | required | green | green | ≥ baseline | ≤ baseline+2x | HammerDB DDL replay parses; **initdb bootstrap SQL replay** (21 importer files exercise it) |
| P6 utility | required | green | green | ≥ baseline | ≤ baseline+2x | pgbench simple-update path exercises SET/BEGIN; initdb replay |
| P7 cutover | full corpus | green | green | ≥ baseline | ≤ baseline+2x | tpch-spotcheck + tpcds SF0.5 + full regress sweep |

Wrapper-routing invariant (from 03 §2) is asserted by a dispatch unit test
so later edits cannot route an inner statement out of an unrouted wrapper.

## 4. Regression policy

* Any red gate stops the wave; fix-forward within the wave or revert the
  flip commit (03-strangler §5). No committing over red.
* Flaky differential comparisons are treated as failing (no retry loops).
* The `-count=1` cache rule from AGENTS/CLAUDE guidance applies: never on
  gate invocations.

## 5. New-test conventions for sqlparser package

* Grammar-level table tests live next to support code
  (`internal/sqlparser/*_test.go`), driving `sqlparser.ParseOne(input)` and
  asserting on constructed AST structs — mirroring how legacy parser tests
  assert today.
* Error-path tests pin SQLSTATE + message + byte Position for representative
  malformed inputs per statement class (guards the error contract,
  01-architecture §6).
* Every GOOPG-EXT rule gets at least one positive and one negative test.
