(idle — nothing in flight)

## Loop summary (2026-07-10, loop #15)

**Outcome: closed the LIVE-path expression-index `indexprs` gap deferred by
loop #14. `pg_get_expr(indexprs, indrelid)` on an expression index now returns
the deparsed expression text instead of NULL. Real feature fix, gated,
committed.**

- Task: `unimplemented_feat #135 (pg_get_expr, indexprs slice)` — the deferred
  slice from loop #14's ledger row.
- Added shared `catalog.IndexExprsText(idx) (string, bool)`
  (`internal/catalog/catalog.go`): joins `idx.ColExprStrings[i]` for each
  expression key column (`Columns[i]==""`, ordinal-0 in `indkey`) verbatim,
  comma-separated; `("", false)` when none → caller emits `VirtualNull`.
  Wired into `PGIndexRowsForDBOid`.
- Byte-matched to PG 18.3: `(lower(b))`→`lower(b)`, `((a+c),upper(b))`→
  `(a + c), upper(b)`, `(a,(a*c))`→`(a * c)`, plain→NULL. The natural deparse in
  `ColExprStrings` already carries the parens — an earlier draft reusing
  `indexKeyIsBareFuncCall` double-wrapped binexprs into `((a + c))`, corrected
  to a verbatim join.
- **Heap twin deliberately unchanged (decode landmine):**
  `buildUserPGIndexRow` still writes `indexprs=NULL`. `DecodePGIndexPhysicalRow`
  (`internal/catalog/codec.go`) infers `indpred` from the bytes after
  `indoption` assuming `indexprs` is NULL — two consecutive nullable varlenas,
  and the decoder gets no tuple null bitmap. Writing a non-NULL indexprs would
  corrupt an expression index's `indpred` on restart. Deferred to a
  null-bitmap-aware decoder (ledger row appended).
- Tests: `internal/executor/pg_index_indexprs_test.go`
  (`TestPgIndexIndexprsExpressionIndex` E2E through pg_get_expr +
  `TestIndexExprsTextParenAndNullRules` helper unit).
- Bookkeeping: `unimplemented_feat.json` #135 code_audit narrowed (surgical
  Edit, JSON valid); deferral-ledger row appended; fix_plan.md `[x]`; design doc
  `0122-0019-*` Follow-up section + README index row updated.
- Gates (foreground, all PASS): `go build ./...`, `go vet` (catalog+executor);
  `go test ./internal/catalog/... ./internal/executor/...`;
  `scripts/tpch-spotcheck.sh` (Q12=2/Q13=33);
  `RALPH_PRECOMMIT_SCOPE=smoke ralph-precommit-test.sh` (0 failed, 3 workloads).

**Next natural work:** the heap-persist `indexprs` slice (needs a
null-bitmap-aware `DecodePGIndexPhysicalRow` + all its callers; also
`Index.ColExprStrings` isn't WAL/heap-persisted for restart yet). OR continue
the `unimplemented_feat.json` survey. OR M0122-0008 (auth/roles/multi-DB).

In-flight: none
