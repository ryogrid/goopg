Task: M0134-0070 (strings.sql) — regress-sql `failed`, still in progress.
This loop landed the `unistr.go` Pos-strip fast-follow flagged by the prior
loop's ledger row (item 1 of that row's "deferred" list).

Files this loop: `internal/executor/unistr.go` (removed `Pos: pos` from all 5
`ExecError` raise sites: `unistrDecode`'s `invalidPair` closure + its default
"invalid Unicode escape" branch; `unistrApplyCodepoint`'s "invalid Unicode
code point" branch and its two "invalid Unicode surrogate pair" branches),
new `internal/executor/unistr_error_position_test.go`
(`TestUnistrErrorsCarryNoPos`, 3 cases). Also `.ralph/deferral_ledger.md`
(new resolved row, 2026-08-21) and `.ralph/fix_plan.md` untouched (no entry
needed a tick — M0134-0070 stays open, this was a sub-item).

Key symbols: `unistrDecode`, `unistrApplyCodepoint`
(`internal/executor/unistr.go`) — the 5 sites that lost `Pos:`. `pos int`
params left in place, unused (legal Go, avoids signature churn); single call
site unaffected (`expr.go:12900`).

Hypothesis/Findings: confirmed via PG oracle
(`postgres/src/backend/utils/adt/varlena.c:6762-6929`, `unistr` function) —
all 5 `ereport(ERROR, ...)` sites in PG's `unistr()` never call
`errposition()`, matching the established pattern from the BinaryExpr/
UnaryExpr/arithmetic() fix two loops ago. No sibling/compiled-twin exists for
`unistrDecode`/`unistrApplyCodepoint` in `exprnode.go` — single call site,
no Rule-4 sync needed. Two other LINE/caret gaps from the same ledger row
remain unfixed (see next step).

Next step: pick ONE of the two remaining LINE/caret fast-follows flagged in
the deferral ledger's 2026-08-21 M0134-0070 rows: (a) `roundNumericToInt`
(numeric→int8 CAST path, expr.go) — needs literal-vs-column AST
classification (harder, PG only sets Pos for bare-literal casts at parse
time, not column-derived casts; not yet independently reverified); (b)
`abs()`/`gcd()`/`lcm()`/`mod()` FuncCall builtin sites in expr.go
(~11882/12135/12138/12167/12170 per a prior implementer report) — same
"pure runtime, no PG errposition" shape as the already-fixed sites, just a
different call-site family (evalFuncCall not BinaryExpr/UnaryExpr). (b) is
likely the smaller/safer next slice, matching this loop's and the prior
loop's sizing pattern. Alternatively pick a fresh `strings.sql` REFACTOR-tier
bucket (Unicode-escape/bit-string/hex-string literals; POSITION/OVERLAY/
LIKE ESCAPE/SIMILAR TO grammar; regexp_count/regexp_like/regexp_instr/
regexp_substr family; regexp_replace backreferences; regexp_matches(...,'g')
multi-match; regexp_split_to_table). Re-verify against the fix_plan banner
(M0134 next-priority-after-M-NIGHTLY) at the START of next loop; also
re-check `ci/logs/action-items.md` for new `## AI-` items (none found this
loop — nightly run 20260821-002906 was `status: pass`, `items: 0`, only
non-blocking env-drift notices about newly-SKIPPED TestPort_* tests, no
action required).

Gates run this loop: `go build ./...` PASS; `go vet ./internal/executor/...`
clean; `go test ./internal/executor/... -run 'TestUnistrErrorsCarryNoPos'`
PASS; full `go test ./internal/executor/...` PASS (7.1s, implementer);
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS
(coordinator, direct run — all packages ok, ~446s for internal/initdb cold
cache, rest cached); pre-commit pgbench smoke runs via git hook at commit
time (result folded into this loop's commit).

Delegation: implementer agent `a74a5eb2af2f0cfce` (1 round — landed the fix
exactly per brief, no deviations, oracle re-check confirmed premise, all
gates green). No further rounds needed.

In-flight: none. Commit pending (staged, about to commit + push to
`regress-renumbering`). No server left running.
