# Deferral Ledger Sample

| status | date | task-id | landed | deferred | resume point | why |
|--------|------|---------|--------|----------|--------------|-----|
| resolved | 2026-06-13 | M0100-0006b | part (c): `(step notices N)` blocker parsing + runner wait | parts (a)/(b): spectoken/transactionid rows not surfacing | `internal/executor/spec_insert_registry.go` | spectoken pg_locks reporting integration |
| resolved | 2026-07-10 | M0122-0024 | `CREATE TABLE name OF type_name (column_name WITH OPTIONS column_constraint [...])` now works | The `TableConstraint` half — a bare `PRIMARY KEY (cols)`/`UNIQUE (cols)`/`CHECK (expr)`/`FOREIGN KEY ...` — is explicitly parse-rejected | `internal/parser/ddl.go`, the `if p.cur().Kind == TokenKeyword` block | Table-level constraints touch the same ~600-line inline loop |
| - | 2026-07-15 | M-NIGHTLY | `evalBinary`'s `parser.OpConcat` case now uses DateStyle-aware formatting | `Datum.Format()`'s remaining ~19 direct call sites are still DateStyle-unaware | `internal/executor/operators_join_agg.go`'s `array_to_string`/array-literal-building FuncCall arms | Kept to `||` only for ONE-task-per-loop discipline |
