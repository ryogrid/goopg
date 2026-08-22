Task: M0134-0070 (strings.sql) — regress-sql `failed`. This loop landed the
`escape_string_warning` GUC no-op registration: `SET`/`SHOW escape_string_warning`
now succeed instead of "unrecognized configuration parameter". Code commit
`85202cee` (+ this bookkeeping commit), both pushed.

Landed: `internal/utils/misc/defaults.go` `BuildDefaultRegistry()` registers
`escape_string_warning` (bool, BootVal "on", ContextUserset,
ScopeSession|ScopeTransaction, no FlagReport — PGC_USERSET/boot_val true/
flags NULL per guc_tables.c:1844-1851). `postgresql.conf.sample` comment line.
New test `internal/utils/misc/escape_string_warning_test.go` (2 funcs). The
lexer-side deprecation warning + `standard_conforming_strings=off`
backslash-lexing stays unimplemented (deferral ledger row 2026-08-22 —
REFACTOR-tier, deliberately NOT conflated with this trivial registration).

Key symbols: `internal/utils/misc/defaults.go` `BuildDefaultRegistry()`
(~line 96, new `escape_string_warning` `r.MustRegister(NewVariable(...))`
block); `internal/utils/misc/escape_string_warning_test.go`.

Next step: continue M0134-0070. Remaining CONTAINED buckets (re-verify sizes
against a FRESH regress run before picking — the ~691-line diff may already
include this GUC registration in-tree): (1) bytea↔int2/int4/int8 casts
(~138 lines) — needs a NEW width-disambiguation mechanism on CastExpr (bytea
casts don't carry source-int-width), similar to the to_hex/to_bin/to_oct
`ArgWidth` plan-stamp precedent — bigger lift, may need decomposing; (2)
sha224/sha384 missing + sha256/sha512 wrongly return TEXT not BYTEA (~60
lines) — needs `exprType` wire-type stamps (same trap as get_bit/get_byte).
REFACTOR-tier buckets (do NOT brief as one implementer round):
`standard_conforming_strings=off` lexing + escape_string_warning warning path
(now ledger-rowed), UESCAPE diagnostics residuals, RE2-vs-ARE regex gaps
(ledger-tracked), ascii()/bit_count() psql-column-width wire-trace
(ledger-tracked), pg-oracle-diff.sh `initdb -q` infra.

Gates run this loop: `go build ./...` PASS; `go test ./internal/utils/misc/
-run TestEscapeStringWarning -v` PASS (tester, both funcs); pre-commit pgbench
smoke PASS (380/702/13222 TPS, 0 failed) via git hook. No full regress re-run
this loop. `make ralph-state-guard` — to run before status block.

Delegation: tester round (m0134-0070-escape-string-warning-guc, verify-only)
DONE — build + focused test PASS. The WIP was written by a prior session's
implementer (Round 1 in the same handoff dir) and left uncommitted when that
loop was cut off; this loop verified + committed it.

In-flight: none.
