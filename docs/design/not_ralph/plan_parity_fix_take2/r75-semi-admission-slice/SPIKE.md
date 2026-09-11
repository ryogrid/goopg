# R75 P0 outcome: PASS — admission needs no new machinery

Keeper: `internal/optimizer/semiadmission_test.go`
(`TestSemiAdmissionFilesPricedNLI`).

- Fixture: outer scanRel (10000 rows) + `nliInnerRel` parameterised
  index probe + `mkSJ(JoinSemi)` + equi clause — all existing
  harness (`joinpaths_test.go`, `joinpathsnli_test.go`,
  `specialjoin_test.go`).
- Result: `addPathsToJoinrel` files a SEMI NLI path —
  `rows=2300 total=160675.00 (outer 200.00)` — satisfying Total ≥
  outer Total (PG `costsize.c:3267/:3307` inequality).
- No BLOCKED piece: parameterised-inner construction in-harness
  works (`nliInnerRel`); the `:341` shared costing prices SEMI
  unchanged; `nestloopOnly` needs no new rule.
- Subset green: 17 passed (`TestSemiAdmission|TestNLIArm|
  TestAddPaths_SemiAnti|TestJointypeForDirection`).

P1 (R76) proceeds on the integration splice only: the arms admit and
price today — remaining work is relset/sjinfo construction for the
unnested relset + replacing the legacy Join with the search-built
subtree through the `stampPlanCost` funnel. No cost-term work.
