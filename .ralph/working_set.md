Task: **M0125-0006** — set-op chains re-associate right when branches are
parenthesised. Code + tests + docs COMPLETE; awaiting the SF0.5 gate, then commit.

Files: `internal/parser/ast.go` (`SelectStmt.ParenBranches`),
`internal/parser/select.go` (reset at `')'`, record boundary on absorb),
`internal/planner/planner.go` (`setOpSegment.cutAt`, `parenBoundary`,
`setOpBindsTighter`, cutAt-keyed save/restore),
`internal/parser/setop_paren_assoc_test.go` + `internal/executor/setop_paren_assoc_test.go` (new),
`docs/design/0125-0006-setop-chain-associativity.md` (new) + `docs/design/README.md`,
`.ralph/fix_plan.md` (M0125-0006 ticked; M0125-0016..-0019 filed),
`.ralph/deferral_ledger.md` (4 rows).

Key symbols: `parseParenthesisedSelectStmt`, `parenBoundary`,
`setOpBindsTighter`, `setOpSegment.cutAt`, `planSelect` set-op fold.

Findings (do NOT re-derive):
- A bool CANNOT express this. Refuted by PG probe: `X UNION (A EXCEPT B) UNION C`
  = `{2,3,9}`, but flag-cleared/fully-left-deep = `{2,9}`. Parens wrap a PREFIX.
- The reset of `ParenBranches` at the closing `')'` is load-bearing for
  `((B) EXCEPT (C))` — the outer paren really does cover the inner absorb.
- Q87: **47218 → 47049 = PG**, same SF=1 data dir, pre-fix binary rebuilt from
  `6c5c48ae`. 1 row either way — no row-count gate can see this class.
- 4 surviving goopg-vs-PG diffs are ALL pre-existing (verified identical on
  pre-fix HEAD): bare-chain precedence (M0125-0016), ORDER BY/LIMIT hoisted out
  of a parenthesised first branch (M0125-0017), IN-list/EXISTS rejecting a
  parenthesised chain (M0125-0018), `string_agg(… ORDER BY …)` ignored (M0125-0019).
- `make plan-diff LABEL=tpcds-round2-head` is **UNRUNNABLE**: that baseline is
  M0124-0002's deliverable and does not exist. Diffing `r5-default` is
  meaningless (it was captured stats-loaded; a fresh server is S-cold, so all
  22 differ on `(stats)`/`rows=` alone). Substituted a same-state pre/post A/B:
  **22/22 TPC-H plans byte-identical** (`analysis/m0125-0006/m0125-0006-{pre,post}.txt`).
- Only 5 TPC-DS queries can reach the changed path (a `)` before a set operator):
  Q8, Q14, Q23, Q49, Q87. TPC-H has ZERO set operators.

Next step: read `analysis/m0125-0006/sf05-sweep.log` for the final
`PASS=/CKMISMATCH=` line; compare against the last known
`PASS=74 CKMISMATCH=4` (M0125-0011). Then commit by explicit pathspec and push.

Gates run: units suite PASS; `scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35);
gofmt clean on my hunks (all 3 files already dirty at HEAD in unrelated regions —
never `gofmt -w`); `go vet` clean; 26 new tests PASS; **gate proved to FAIL at
`6c5c48ae`** (10 subtests fail, every non-regression pin passes); TPC-H plan A/B
identical 22/22. SF0.5 sweep IN FLIGHT.

In-flight: `FORCE=1 bash scripts/tpcds-sf05-regression.sh sweep`, log
`analysis/m0125-0006/sf05-sweep.log`, results
`bench/tpcds/runtime_goopg/tpcds-results-sf05/sweep-*.txt`. NOTE: a first attempt
was killed at Q5 — its server hit **21 GB RSS** (the documented
non-cancelling-star-query hazard, pre-existing) and the host reached 0 GB
available. If it must be killed again: `kill -TERM <sweep pid>`, then
`tmp/goopg-bench-bin stop -D <repo>/bench/tpcds/runtime_goopg/data-sf05` —
the server is orphaned by the script's death and holds the RAM.
**Never `pkill -f`** — it self-matched and killed my own shell (exit 144) once
this loop.
