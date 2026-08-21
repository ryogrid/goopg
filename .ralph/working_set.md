Task: M0134-0070 (strings.sql) — regress-sql `failed`, still in progress.
This loop found the prior loop's uncommitted WIP already on disk (matched
its own "next step" plan item (b): abs/gcd/lcm/mod Pos-strip) — verified it,
appended the deferral-ledger row, and committed + pushed it.

Files this loop: `internal/executor/expr.go` (stripped `Pos: x.Pos()` from 4
`ExecError` raise sites in `evalFuncCall`'s math-builtin arms: `abs()`
bigint-overflow/MinInt64, `gcd()`/`lcm()` bigint/integer-out-of-range [2
sites], `mod()` division-by-zero), new
`internal/executor/abs_gcd_lcm_mod_error_position_test.go`
(`TestAbsGcdLcmModErrorsCarryNoPos`, 4 cases). `.ralph/deferral_ledger.md`
(new resolved row, 2026-08-21, closes item (3) from the prior row's
resume-point list). Left untouched (unrelated concurrent WIP present in the
tree at loop start, per CLAUDE.md never-git-add-A): `.ralph/progress.json`,
`ci/logs/scheduler.log`, `third-party/tpcds-postgres` submodule ref,
untracked `postgres` symlink.

Key symbols: `evalFuncCall` (`internal/executor/expr.go`) — the 4 math-
builtin arms that lost `Pos: x.Pos()`. No compiled-twin duplicate of these
arms in `exprnode.go`, so no Rule-4 sibling sync needed (confirmed by the
implementer that did the original slice, re-verified this loop).

Hypothesis/Findings: confirmed via PG oracle (`postgres/src/backend/utils/
adt/int.c`, `int8.c`, `numeric.c`) — the overflow/division-by-zero
`ereport(ERROR, ...)` sites backing `abs()`/`gcd()`/`lcm()`/`mod()` never
call `errposition()`, same "pure runtime FuncCall evaluation" shape as the
already-fixed BinaryExpr/UnaryExpr/unistr sites from the prior two loops.
This closes item (3) from the 2026-08-21 M0134-0070 ledger row's resume-
point list. Item (2), `roundNumericToInt` (CAST literal-vs-column
classification), remains open — harder, needs an "is the erroring operand a
bare `*optimizer.Const`" check, not yet independently reverified.

Next step: pick ONE of: (a) `roundNumericToInt` (numeric→int8 CAST path,
expr.go) — the remaining harder LINE/caret item; (b) re-run
`scripts/pg-regress-runner.sh --verbose strings` to get the post-fix
diff-line count (not measured this loop — last known was 2501 lines before
this fix) before sizing the next bucket; (c) a fresh `strings.sql`
REFACTOR-tier bucket (Unicode-escape `U&'...'`/`UESCAPE` + bit/hex-string
literals; POSITION/OVERLAY/LIKE ESCAPE/SIMILAR TO grammar; regexp_count/
regexp_like/regexp_instr/regexp_substr family; regexp_replace
backreferences; regexp_matches(...,'g') multi-match; regexp_split_to_table).
Re-verify against the fix_plan banner (M0134 next-priority-after-M-NIGHTLY,
confirmed unchanged this loop) at the START of next loop; also re-check
`ci/logs/action-items.md` for new `## AI-` items (none found this loop —
same nightly run 20260821-002906 as the prior loop, `status: pass`,
`items: 0`, only non-blocking env-drift notices, no action required).

Gates run this loop: `go build ./...` PASS; targeted
`go test ./internal/executor/... -run 'TestAbsGcdLcmModErrorsCarryNoPos|
TestUnistrErrorsCarryNoPos'` PASS; full `go test ./internal/executor/...`
PASS (cached); `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
PASS (coordinator, direct run, ~435s for internal/initdb cold cache, rest
cached); pre-commit pgbench smoke ran via git hook at commit time — PASS
(364/671/12498 TPS across the 3 pgbench modes, 0 failed transactions).
`make ralph-state-guard` — run before this status block, see below.

Delegation: none this loop (no new implementer round — the diff was already
complete on disk from a prior session that hadn't reached commit; this loop
only verified + bookkept + committed). No open handoff.

In-flight: none. Commit `ef633438` landed and pushed to
`regress-renumbering` (`c4e230eb..ef633438`). No server left running.
