# Working set — M0134-0001 aggregates.sql (class-10 VERBOSE qual → REVERTED, blocked)

**Task:** M0134-0001 aggregates.sql EXPLAIN-format digestion (P2). This loop
attempted the ranked slice #2 (class 10 VERBOSE `Output:`/`Group Key:`/`Sort Key:`
qualification) and found it **blocked** — reverted with zero gate impact.

**Status:** class-10 slice REVERTED (implementer change `git checkout`-ed clean).
Design doc corrected (`docs/design/0134-0001-p2-explain-format.md` new
"class 10 … blocked by the varno deferral" section). Nothing landed this loop.

**Finding (decisive, refutes the S2 out-of-scope note):** adding SRF cases to
`explainRelBaseName` + qualifying the VERBOSE `Output:` line does NOT close the
aggregates.sql SRF hunks, because those hunks are all LATERAL/subquery shapes:
`nextSourceIdx` restarts at 1 per query level, so `generate_series s2` collides
with outer `s1` and `explainNames.collect`'s `seen` guard registers only one →
`qualify()` false → bare columns. This is the **existing M0125-0039 deferral**
(ledger row 615 "SourceTableIdx is not a range-table id"; row 616 (b)
schemaColumnNames never reaches the expression printer). Class 10 needs the
planner-level per-statement `nextSourceIdx` promotion, not formatter work.

**Files:** `docs/design/0134-0001-p2-explain-format.md` (class-10 blocked note).
`internal/executor/{explain_names,operators_explain,explain_function_scan_test}.go`
— reverted to HEAD.

**Key symbols:** `explainNames.collect`/`register`/`qualify`/`column`
(explain_names.go); `schemaColumnNames` (operators_explain.go:1284); `planner.go`
`nextSourceIdx` (per-query-level restart — the real blocker).

**Remaining slices (ranked):** (3) **S3 join-label drop** — next; (6) S8
GroupAggregate + multi-rel GROUP BY (cost-model, largest); (d) scalar-subquery
nesting (hard/coupled); (e.2) inheritance MergeAppend (dead-end). Class 10 is
blocked behind the varno deferral (ledger 615) — not a formatter slice.

**Next step:** brief + delegate **S3 (class 7a)** — drop the `(INNER)`/`(CROSS)`
suffix from join node labels; PG annotates only NON-inner joins (` Left Join`,
` Semi Join`, …). Files: `internal/executor/operators_explain.go` `describePlan`
join case (~1590-1609, 1739-1744) + `joinTypeName` (~1835). Oracle:
`explain.c` suffix-only-for-non-inner (1754-1763). Touches EVERY join plan line,
so verify across `scripts/pg-regress-runner.sh aggregates` + `scripts/tpch-spotcheck.sh`
+ TPC-DS SF0.5 plan-gate (plan-shape text changes suite-wide).

**Gates run (this loop):** `go test ./internal/executor/` PASS (implementer,
pre-revert); `scripts/pg-regress-runner.sh aggregates` 1370→1370 (zero delta —
the blocked proof); tpch-spotcheck NOT run (nothing landed).

**Delegation:** implementer `0134-0001-class10-verbose-qual` (a8b4a71e) DONE →
NEEDS-DECISION → reverted. Brief/report under `tmp/ralph-handoffs/0134-0001-class10-verbose-qual/`
(scratch).

**In-flight:** none.
