Task: M0134-0070 (strings.sql) — regress-sql `failed`. This loop landed the
`ascii()`/`crc32`/`crc32c`/`bit_count` wire-TypeOID bucket (the recorded deferred
"ascii()/bit_count() column-width" bucket, root-caused: NOT a wire-trace, a
4-case exprType change). Code commit `e8eb7214` (+ bookkeeping), both pushed.

Landed: `exprType` (`internal/optimizer/planner.go` ~12598) gained
`case "ascii": -> int4` and `case "crc32","crc32c","bit_count": -> int8` arms
mirroring the `get_byte`/`get_bit` arm, so the wire layer (`typeOIDFor`,
`internal/postmaster/dispatch.go:3639`) advertises int4/int8 instead of text
(default `unknown`→OID 25), making psql right-align these numeric columns
(`column_type_alignment`, print.c:3614-3638). No sibling/twin — all RowDescription
paths call `typeOIDFor(sc.Type)`. New test
`internal/optimizer/expr_type_wire_test.go`. `strings.sql` diff shrank 451→395
lines (three hunks closed); cross-cutting (also fixes misc_functions/domain/stats).

Key symbols: `internal/optimizer/planner.go` `exprType` FuncCall switch
(~12598-12608, new arms), `get_byte`/`get_bit` arm (the mirror pattern),
`internal/postmaster/dispatch.go` `typeOIDFor` (:3639).

Hypothesis/Findings: the researcher retriage confirmed the recorded four-bucket
list and re-ranked it. Remaining 395-line diff buckets (counts from the retriage):
`standard_conforming_strings=off` lexing + `escape_string_warning` (REFACTOR-tier,
69); RE2-vs-ARE regex backrefs/zero-width/SIMILAR-ambiguity (28); Unicode-escape
error-message/DETAIL text (16); `char(20) '...'` typed-literal grammar (14); SQL99
`SUBSTRING FROM..FOR` + missing CONTEXT (8); regexp error-path HINT (7);
**lpad/rpad empty-fill value bug (6)**; bytea LIKE (6); toasttest
`pg_relation_size` NULL (2). Deferred (ledger 2026-08-22): `bit_count(bit)`
overload (OID 6163) unimplemented at runtime.

Next step: the `lpad`/`rpad` empty-fill value bug — smallest remaining CONTAINED
value-bug (~6 lines). `internal/executor/expr.go:15049-15051` and `:15077-15079`
do `if fill == "" { fill = " " }`; PG does NOT pad (oracle_compat.c:196-197
`if (s2len <= 0) len = s1len;`). Brief + implement this next.

Gates run this loop: `go build ./...` PASS; `go test ./internal/optimizer/ -run
TestExprTypeWireTypeOID -v` PASS (6/6); `go test ./internal/optimizer/
./internal/executor/` PASS; `scripts/pg-regress-runner.sh --verbose strings`
451→395 lines; `\gdesc` wire-flip verified (ascii→23, crc32/crc32c/bit_count→20);
pre-commit pgbench smoke PASS (357/617/11860 TPS, 0 failed) via git hook.
`make ralph-state-guard` — to run before status block.

Delegation: researcher (m0134-0070-retriage) DONE — root-caused the wire-TypeOID
bucket, no wire-trace needed; implementer (m0134-0070-wire-typeoid) DONE — all
gates PASS, committed as `e8eb7214`.

In-flight: none.
