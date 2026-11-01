# Working Set — loop end 2026-11-01 (loop #15: M0148-0001 partial — children a+b LANDED, census exhausted)
- Task: M0148-0001 `aggregates.sql` closure (item 10). Baseline 40 hunks → 33 after
  0001a (nine `name_or_call` FILTER/WITHIN-GROUP cross-product arms; FILTER pipeline was
  already complete) + 0001b (`nodes.ScanBitInput` varbit.c prefix arms + codec/pgwire/
  cast-time wiring). Commits `0ad18e437` + census record `94cd3a8a7` pushed.
- Files: `grammar/pg_grammar.y` (+regenerated yacc_parser/tokennums), `internal/nodes/datum.go`,
  `internal/executor/codec.go` (validateBitDigits→scanBitInput), `internal/executor/expr.go`
  (string→bit cast arm at the Typmod site), tests `internal/parser/agg_clause_arms_test.go`,
  `internal/nodes/scan_bit_input_test.go`, `internal/executor/bit_string_literal_test.go`,
  design doc `0100-0149/0148-0001-agg-clause-arms-and-bit-prefix.md` + README row, fix_plan
  0001a/b check-offs + NEW children 0001c..j, 2 ledger rows, `analysis/.../routing-20261101d/`.
- Key symbols: name_or_call arms; raiseErr(&SyntaxError{Raw:true, Pos:$<p>8}) for the
  multiple-ORDER-BY-WITHIN raise; nodes.ScanBitInput (prefix b/B, x/X hex×4, 22P02 rune-aware,
  VARBITMAXLEN 54000); executor scanBitInput; evalCast string→bit arm (22026/22001 cast-time).
- Hypothesis/Findings: remaining 33 hunks all filed: plan-shape (scalar-agg InitPlan min/max
  =0001c, GROUP BY FD/pathkey order =0001d, Incremental Sort/Index Cond/SubqueryScan elision
  =0001e, parallel-agg election =0001i), EXPLAIN Output rendering =0001f, pg_get_viewdef
  deparser =0001g, DROP-CASCADE view deps =0001h, DISTINCT-agg drain order =0001j. Runner
  diff `tmp/regress-diffs/aggregates.diff` (891 lines). Gate flakes reproduced on CLEAN HEAD
  under load (documented under the M0148 entry): TestExplainSplitAggregateChainCosts
  (2s absolute conn deadline vs ~1.2M in-process inserts; fails loaded, passes idle/CI) +
  abort-batch/ConnTx instant-fail family (pass isolated).
- Next step: implement **M0148-0001c** (scalar group agg → InitPlan+Limit+Index Only Scan
  Backward; prepagg.c/planner.c scalar-group path + createplan Limit/backward-scan) — first
  plan-shape child; baseline via `scripts/pg-regress-runner.sh -v aggregates` at HEAD
  (33 hunks; hunks at expected-lines 950-1108 are the target).
- Top-residual: M0148 campaign has no census; largest residual class = plan-shape hunks
  (11 of 33, children c/d/e/i); EXPLAIN rendering =7 (0001f). Plan-parity census: EXHAUSTED
  at routing-20261101d (fires=none, 22/22+99/99+99/99, all categories 0).
- Skip-rationale (bypassed items 0-9; item 10 selected): 0 channel (P0-E8 `[x]`), 1/2/2a
  discharged, 3 channel (census consumed 94cd3a8a7, no open plan category), 4-8 channel,
  9 M0147 channel+2 owner-gated `[!]`, 10: M0122-0015h self-records `Rank: below item 10
  under the S7 carve-out` (client-tool conformance, no plan instrument) → live M0148 queue
  head 0001. Five open M-NIGHTLY P0-E9/E10/E11/E13/E15 = ESCALATION, OWNER PLACEMENT NEEDED.
- Gates run: gen-parser conflict gate 60 + goldens unchanged; parser/nodes/executor full
  suites PASS; postmaster full = pre-existing flakes only (clean-HEAD reproduced); runner
  40→33 hunks; tpch-spotcheck Q12=2/Q13=35; tpcds-sf025 sweep PASS; acceptance arm off 24
  MATCH; fireset-scope OUT; precommit smoke PASS (staged); pgbench smoke via hook; census
  DUE → consumed (routing-20261101d, tpcds-fireset stamp PASS on code_tree 02fe56c3ec1c).
- In-flight: none.
