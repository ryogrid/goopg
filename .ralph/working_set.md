# Working set — M0134-0002 alter_table.sql (C15 `col_description` LANDED)

**Task:** M0134-0002 alter_table.sql regress-sql digestion. This loop landed **C15**
— the `pg_catalog.col_description(oid,int4)` builtin: a `case "col_description":`
beside `obj_description` in the executor function-name switch
(`internal/executor/expr.go` :9840), reading `GetComment(1259, objoid, attnum)`
(STRICT, NULL on no-match). pg_description catalog + COMMENT ON already existed;
only dispatch was missing (pg_proc seed OID 1216 pre-present).

**Status:** COMPLETE + committed (code `044057b9`, bookkeeping commit follows).

**Findings:** C15 was the second (and last) of the two `\d+` describe blockers
(C1 array `||` + C15 col_description). 12 `col_description does not exist` errors
→ 0 (519→507 ERROR lines); no new class; diff 4673→4677 lines (+4 — the
previously-masked describe output now renders, same "advance reveals more" pattern
as C1). Both `\d+` blockers cleared; the describe pipeline now runs to completion.

**Files:** `internal/executor/expr.go` (+case); new test
`internal/executor/operators_ddl_col_description_test.go` (returns-comment,
no-match→NULL objsubid 0/2 + unknown OID, STRICT NULL-arg);
`docs/design/0134-0002-alter-table-sql-divergence.md` (C15 row → LANDED);
`.ralph/deferral_ledger.md` row 1390 → `resolved`; `.ralph/fix_plan.md` M0134-0002.

**Key symbols:** `evalFuncCall` switch (`expr.go`), `im.GetComment(1259, objOID,
attnum)` (`catalog.go`), `obj_description` sibling arm (:9815-9838).

**Next step:** C2 — the ALTER-TABLE grammar cluster (largest remaining class:
`RENAME CONSTRAINT`/`RENAME <col> TO`/`TYPE … USING`/comma multi-action/`NO
INHERIT`/`NOT VALID`/`DROP COLUMN IF EXISTS`/`SET WITHOUT OIDS`/`STORAGE`/
`ANALYZE tab(col)`/ENFORCED dup; `internal/parser/ddl.go`
`parseAlterTableAction`/`parseOneAttrCmd`/`consumeAttrCmdTrailer`). Needs a fresh
researcher pass to decompose the grammar gaps into per-rule slices before
implementing. Alternatively pick a smaller correctness class (C5 btree-inet, C8
system-columns) for a single-loop win.

**Gates run (this loop):** `go test ./internal/executor/` PASS (3 new tests);
`go build ./...` clean; `scripts/pg-regress-runner.sh alter_table` — 0
col_description errors, 507 total ERROR lines (was 519), no new class;
`scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=35); pre-commit pgbench smoke PASS.

**Delegation:** researcher `0134-0002-c15-coldescription-research` DONE (verdict
builtin-only); implementer `0134-0002-c15-coldescription-impl` DONE; tester
`0134-0002-c15-coldescription-gates` DONE.

**In-flight:** none.
