Task: M0134-0070 (strings.sql) — regress-sql `failed`. This loop landed the
bytea↔intN casts bucket — the last CONTAINED bucket from the sizing pass.
Code commit `c262a3ea` (+ bookkeeping), both pushed.

Landed: all six explicit int2/int4/int8 ↔ bytea casts now byte-exact.
Forward `intN_bytea` via an `evalCastTyped` intercept keyed on the already-
stamped `CastExpr.SourceType` (NOT a new mechanism as the prior loop assumed —
`plan.go:540-546`, set `planner.go:13662`, preserved by fold/remap/shift).
Reverse `bytea_intN` via new `case KindBytes:` in the int2/int4/int8 arms of
`evalCast` + shared `byteaToIntN`/`byteaIntSourceWidth`. No parser/planner/
catalog/fold change needed. New `internal/executor/bytea_int_cast_test.go`
(27 subtests). `strings.sql` diff shrank 599→451 lines.

Key symbols: `internal/executor/expr.go` `evalCastTyped` (~3959, forward
intercept), `evalCast` int2/int4/int8 arms (~4214/4247/4285, KindBytes arms),
`byteaToIntN`/`byteaIntSourceWidth` helpers.

Hypothesis/Findings: the "new width-disambiguation mechanism" the prior loop
anticipated was UNNECESSARY — `CastExpr.SourceType` already carries the source
type. Deferred (ledger 2026-08-22): (1) `typename(expr)` functional-cast syntax
`bytea(int4_col)`; (2) `coerceExecParam` EXECUTE-param bytea arm; (3) bare
`5::bytea` int8-vs-int4 literal-typing quirk.

Next step: re-measure/triage the 451-line `strings.sql` diff (delegate to
researcher) to enumerate the remaining buckets. Expected remaining: the deferred
`ascii()`/`bit_count()` psql-column-width wire-trace (needs a dedicated
wire-trace slice), `standard_conforming_strings=off` lexing +
`escape_string_warning` warning path (REFACTOR-tier, ledgered), residual
Unicode-escape error-message/DETAIL mismatches, deferred pgcrypto `digest()`
hex-vs-bytea. Pick the next contained fix.

Gates run this loop: `go build ./...` PASS; `go test ./internal/executor/ -run
TestByteaIntCast -v` PASS (27/27); `go test ./internal/executor/
./internal/optimizer/` PASS; `scripts/pg-regress-runner.sh --verbose strings`
599→451 lines; pre-commit pgbench smoke PASS (379/693/12883 TPS, 0 failed) via
git hook. `make ralph-state-guard` — to run before status block.

Delegation: researcher (m0134-0070-bytea-int-casts sizing) DONE; implementer
(m0134-0070-bytea-int-casts, Round 1) DONE — all gates PASS, committed as
`c262a3ea`.

In-flight: none.
