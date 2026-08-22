Task: M0134-0070 (strings.sql) — regress-sql `failed`. This loop landed the
sha-digests bucket: sha224/sha384 added + sha256/sha512 fixed to return bytea.
Code commit `4109aea4` (+ this bookkeeping commit), both pushed.

Landed: `internal/executor/expr.go` `evalFuncCall` now has FOUR sha arms
(`sha224`=`crypto/sha256.Sum224`, `sha256`=`.Sum256`, `sha384`=
`crypto/sha512.Sum384`, `sha512`=`.Sum512`), each returning `NewBytesDatum(h[:])`
(KindBytes) instead of hex TEXT; input mirrors the sibling `crc32` arm
(`BytesValue()` + `byteaIn` fallback for non-bytea Kind). `exprType`
(`internal/optimizer/planner.go`) gained `case "sha224","sha256","sha384",
"sha512" -> bytea` (mandatory — the builtin pg_proc seed does not feed
`ReturnType`). `isKnownBuiltinFunction` (`internal/executor/operators_call.go`)
gained sha224/sha384/sha512. New `internal/executor/sha_test.go` (8 subtests).
`strings.sql` diff shrank 691→599 lines, grep-confirmed zero residual sha
divergence. sha224/sha384 were already catalogued (OID 3419-3422) — only the
executor switch arms were missing.

Key symbols: `internal/executor/expr.go` `evalFuncCall` (~10597 sha arms);
`internal/optimizer/planner.go` `exprType` (~12587 sha→bytea case);
`internal/executor/operators_call.go` `isKnownBuiltinFunction` (~753).

Hypothesis/Findings: the sha bucket was fully CONTAINED (3 files + 1 test, zero
catalog changes). Deferred (ledger 2026-08-22): the adjacent pgcrypto `digest()`
arm still returns hex TEXT not bytea — same class, out of strings.sql scope.

Next step: continue M0134-0070. The last CONTAINED bucket is bytea↔intN casts
(int2/int4/int8 ↔ bytea, ~138 lines at the prior sizing). It needs a NEW
width-disambiguation mechanism on CastExpr (bytea casts don't carry
source-int-width) — same `ArgWidth` plan-stamp precedent as the to_hex/to_bin/
to_oct work (internal/optimizer/plan.go `FuncCall.ArgWidth` + a `resolveExpr`
intercept). Bigger lift than sha; size it via researcher first (does the regress
fixture distinguish int2/int4/int8 source widths? what does PG's bytea↔int cast
set look like in pg_cast/pg_proc?). REFACTOR-tier buckets (do NOT brief as one
round): `standard_conforming_strings=off` lexing + escape_string_warning warning
path (ledger-rowed), ascii()/bit_count() psql-column-width wire-trace
(ledger-tracked), the deferred digest() hex-vs-bytea.

Gates run this loop: `go build ./...` PASS; `go test ./internal/executor/
-run TestSha -v` PASS (8/8); `go test ./internal/executor/ ./internal/optimizer/`
PASS; `scripts/pg-regress-runner.sh --verbose strings` 691→599 lines; pre-commit
pgbench smoke PASS (381/704/13144 TPS, 0 failed) via git hook. `make
ralph-state-guard` — to run before status block.

Delegation: researcher (m0134-0070-sha-digests sizing) DONE; implementer
(m0134-0070-sha-digests, Round 1) DONE — all four gates PASS, tree left
uncommitted and committed by coordinator as `4109aea4`.

In-flight: none.
