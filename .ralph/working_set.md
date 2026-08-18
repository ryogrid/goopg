# Working set — M0134-0005j landed (plan-time DEFAULT for omitted columns, −98 lines)

**Task:** M0134-0005 (`constraints.sql`) — **M0134-0005j LANDED**. Sub-item `[x]`,
parent case stays `[ ]`. Selected per the Current Priority banner (M0134 after
M-NIGHTLY). M-NIGHTLY drained: `ci/logs/action-items.md` still at run
`20260818-005518`, **items: 0** — nothing to file.

**What landed:** a column omitted from an **explicit** INSERT column list silently
lost its DEFAULT (`z INT DEFAULT -1 * currval('seq')` came out NULL, which also let
`CHECK (x+z=0)` accept the violating row — a NULL predicate passes; PG rejects only
FALSE). `rewriteInsertDefaultMarkers` (`internal/optimizer/planner.go`) now
substitutes `col.DefaultExpr` at plan time for that shape too, not just when
`len(s.Columns)==0`.

**Four things worth not re-learning:**
- **It was a PLANNER gap, not an evaluator gap.** goopg has TWO default-expression
  evaluators; the crippled one (`applyDefaultsForMissing`/`evalGenFuncCall`,
  `operators_generated.go:239`) is reached only for the one shape the planner
  skipped. **Fixing the planner was chosen over adding `currval` to the
  mini-evaluator** — the 10-line patch would leave a second evaluator that silently
  NULLs the *next* unhandled builtin. Do not undo this reasoning.
- **Fixing the producer fixed both consumers for free.** Plain INSERT and ON CONFLICT
  are a Rule-#2 twin pair; the plan-time fix covers both by construction, plus the
  `nextval`-bypasses-`ctx.CurrSeqVals` off-by-one. Fifth "unwired, not missing" in
  this milestone — probe the producer before any "needs new infrastructure" claim.
- **Two hypotheses refuted by measurement.** CHECK evaluation is already PG-correct
  (`operators_fk.go:1699` `continue`s on NULL), and the briefed **serial
  double-advance hazard never materialised** (a serial column's catalog
  `DefaultExpr` is nil ⇒ excluded by construction). Both were probed, not assumed.
- **−98 lines vs a ~26-line estimate.** The census attributes by hunk, and one hunk
  bundled four independent bugs. Treat bucket line-counts as lower bounds.

**Gates run:** `go build ./...`; `go test ./internal/optimizer/ ./internal/executor/`;
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS;
`scripts/tpch-spotcheck.sh` PASS (**Q12=2, Q13=35** — Rule #1);
`go test -run 'TestPort_.*(Insert|Default|Copy|Sequence)' ./internal/testport/` PASS;
9 new guards FAIL-pre/PASS-post by stashing only `planner.go`; pre-commit pgbench
smoke PASS via the hook. Test cache was warm throughout.

**Next step — pick from the §16.1 census, re-measured at the new 1024-line/31-hunk
baseline.** The three freshly ledgered rows are the strongest candidates and are all
the SAME subsystem, so one slice could take them together: `INSERT … SELECT` with an
explicit column list (the `s.Select != nil` early return in
`rewriteInsertDefaultMarkers`), `COPY` with a partial column list dropping *every*
DEFAULT (`copy.go:323` never calls the filler), and the apply worker
(`applyworker.go:286`) as the last live caller of the mini-evaluator. Otherwise the
remaining census buckets are 3 (GiST `circle_ops`, pre-existing), 4 (multi-unnamed
CHECK auto-naming + COPY not re-checking CHECK), 7 (grammar, tiny), 8 (float8
formatting, ~2 lines) — plus the three unresearched bugs sharing bucket 2's hunk
(inherited-CHECK naming on `INSERT_CHILD` ~19 lines, `tableoid` in a CHECK ~15,
`ctid`-in-CHECK DDL rejection ~5). **Regenerate the census before briefing** —
`GOOPG_CG_UNIT=<n> scripts/pg-regress-runner.sh --verbose constraints`.

**Delegation:** `tmp/ralph-handoffs/m0134-0005j-probe/` (researcher
`a232b33a67898c987`, 2 rounds, DONE — pinned the planner gap, answered the
retire-the-mini-evaluator feasibility question);
`tmp/ralph-handoffs/m0134-0005j-default-omitted-col/` (implementer
`a6e44f925d1b15a8d`, 1 round, DONE); `tmp/ralph-handoffs/m0134-0005j-gates/`
(tester `a1144f5b2f317177f`, DONE).

**In-flight:** none.
