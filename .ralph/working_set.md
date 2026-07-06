(idle — nothing in flight)

Loop #14 completed and committed: closed the M0122-0005 "inline-cast
`\"char\"` value-truncation" residual (deferred item (1) from the
2026-07-05 OID-18-disambiguation row). `internal/executor/expr.go`'s
`evalCast` `"char"` branch now truncates any non-octal-escape input to its
first byte (matching real PG's `charin()`), rendered via the existing
`charTypeDisplayForm`. Disambiguated at the `evalExpr` `*planner.CastExpr`
call site (the only place `Typmod` is in scope): when `TargetType=="char"
&& Typmod>0` (bare `char`/CHARACTER, grammar-synthesized to a distinct
bpchar(1) cast sharing the same TargetType string) the call renames the
target to `"bpchar"` for that one `evalCastTyped` invocation only, so
genuine OID-18 casts (`Typmod==0`) are the sole ones truncated —
`evalCast`'s shared signature untouched. New
`internal/executor/char_oid18_truncation_test.go`:
`TestEvalCastCharTruncatesToFirstByte` (direct unit, incl. octal-escape
precedence) + `TestCastExprCharTypmodDisambiguation` (full parse→plan→eval
pipeline via `runQuery`/`newDDLFixture`). Verified: go build/vet clean;
`go test ./internal/executor/... ./internal/planner/... ./internal/parser/...
./internal/server/... ./internal/catalog/...` all PASS (no regressions,
one server-package flake reproduced as pre-existing/unrelated — passed on
rerun); `scripts/tpch-spotcheck.sh` PASS (Q12=2/Q13=33). Design doc
(`docs/design/0122-0005-char-oid18-disambiguation.md`) new "Follow-up:
inline-cast value truncation" section + README row updated. Ledger row
appended (status `-`): newly deferred is generic bpchar/varchar typmod
truncation/padding in the inline-cast evaluator (materially broader — e.g.
`'xyzzy'::varchar(3)` still passes through unchanged), plus the
pre-existing unrelated `pg_typeof(...)::oid` gap (still open, untouched).

Next candidate (pick ONE): bpchar/varchar typmod truncation in the
inline-cast evaluator (small, same-shape follow-up — resume point:
`internal/executor/expr.go`'s `*planner.CastExpr` case has `x.Typmod` in
scope, same call site as this loop's fix; add a truncate/pad helper for
the `"varchar","bpchar"` cases mirroring the existing numeric/time typmod
block ~L850-879), the view's-own-ACL gap from M0122-0008 (materially
larger — needs a preliminary per-statement RTE-style permission pass),
resume the M0110-0001 multi-database isolation survey (fix_plan "Current
Priority" banner — per-database catalog/storage isolation, milestone-scale,
repeatedly deferred across many loops as too large for one loop), or
survey `.ralph/deferral_ledger.md` for another fresh open (`status = -`)
row.
