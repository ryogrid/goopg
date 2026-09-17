(idle — nothing in flight)

Task: M-NIGHTLY testport/TestPort_RegressSuite time/timetz fix
(AI-20260905-011015-008 chain). Selected via banner item 0 fallback: P0-E6
HOLD still present; M0143 nothing selectable; M0141-S2b-4/S2b-3b are
CONFIRMED non-recon (real implementation) — don't re-check next loop, settled.
Fell through to M-NIGHTLY; nightly run 20260918-010720 had nothing new
(already filed+closed-stale).

Files: `internal/executor/expr.go` (fix), `.ralph/fix_plan.md` (updated entry
+ 2 new tasks).

Key symbols: `evalTypedStringLit` (expr.go:4810, "time"/"timetz" arms
~4941-4969) discarded `parseTimeString`/`parseTimeTZString`'s
(copy_text.go:555,738) already-correctly-typed `*ExecError` (22008 range vs
22007 syntax) and always emitted hardcoded 22007. Sibling
`btree_scalar_keys.go:198-224` already had the right `if ee, ok :=
err.(*ExecError); ok { return v, ee }` guard — classic
pattern_sibling_paths_must_agree drift. Audited all other call sites
(codec.go, evalCast, operators_pg_input_error_info.go) — already correct.
Fix: same guard added to both arms, preserving `x.Pos()`.

False trail (ruled out, ~half the loop): looked like a stdout/stderr
interleaving bug in `ClusterRegressExecutor.ExecuteSQL` (`Stdout+Stderr`
concat can reorder ERROR lines) — but `NormalizeRegressOutput` already
neutralizes this generically (sorts ERROR/NOTICE/etc. to a trailing block,
regress.go:956). Real cause was message-text mismatch, not ordering.

`limit`/`numerology` subtests still fail — unrelated, filed as own M-NIGHTLY
tasks (not touched, ONE task per loop): numerology needs 0b/0o/0x integer
literal parser support (grammar change, read goyacc playbook first); limit
has a FETCH BACKWARD sign/row bug, not yet localized.

Gates run: `go build ./...` clean; `go test ./internal/executor/...` PASS;
targeted `TestPort_RegressSuite/{time,timetz}` PASS; full suite 47 PASS/2
FAIL(limit,numerology, pre-existing)/183 SKIP, up from 45/4/183 — no new
regressions; `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
all PASS; `make ralph-state-guard` auto-repaired stale marker, clean after.
No TPC-H data needed (scalar coercion fix, unrelated to joins/planner).

Next step: re-read banner, re-check P0-E6 HOLD. If still present: (a) check
whether the 2 new M-NIGHTLY tasks (numerology grammar work, limit cursor bug)
are recon or implementation before selecting; (b) else try
race/internal/executor (AI-20260905-011015-001 chain, `go test -race
-timeout 45m ./internal/executor/`, not yet investigated); (c) re-check
ci/logs/action-items.md for a run newer than 20260918-010720 first.

In-flight: none.
