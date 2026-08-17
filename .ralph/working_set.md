# Working set — M0134-0003 S1 landed + case PARKED; pick the next M0134 case

**Task:** M0134-0003 (`arrays.sql`) — **S1 LANDED, umbrella PARKED 2026-08-18**
(commit `bfe0586f`). Selected per the Current Priority banner (M0134 next after
M-NIGHTLY). M-NIGHTLY drained: `ci/logs/action-items.md` is still run
`20260817-011734`, all 6 filed and `[x]` — nothing new to file.

**What landed.** `expr [NOT] LIKE|ILIKE ANY|SOME|ALL (…)` now parses/evaluates
per PG. A **sibling-path omission**, not a missing feature: `parseExprPrec` wires
quantifiers for the ordering comparisons (M0122-0004) and the POSIX regex
operators (M0097-0068) but skipped the LIKE family sitting between them. Fixed in
all four blocks. **Parser-only, verified not assumed** — `evalInExpr`
(`internal/executor/expr.go:6859,6877`) dispatches `AnyOp` through the *general*
`evalBinary`, which already implements all four opcodes at `:1684-1699`.
Measured `arrays` **3311→3251** lines, all 8 statements of `arrays.sql:463-470`
now unchanged context; sentinel `char` 172 unchanged.

**Why parked.** Residual has no bounded slice: **A** slice subscripting
`a[lo:hi]` absent read+write (~900) and **B** assignment-target indirection
`SET col[i]=…` (~250) are ~1150 *coupled* lines sharing one representation goopg
lacks; **C** is 13 array builtins catalog-registered with no executor dispatch
(~600); D′ (~10) and E (~40) are fragments. **Re-arm trigger:** A+B landing as
their own milestone, or class C being filled in — then re-measure.

**Correction to carry forward (do not re-derive):** class D is NOT "generalize
ANY/ALL to arbitrary operators". A precision pass found the only operators
failing with a quantifier in this file are the LIKE family and `*`; no
`@>`/`<@`/`&&`/`~`/`IS DISTINCT FROM` appears with ANY/ALL anywhere here, so a
general rewrite has no corpus witness.

**Files:** `internal/parser/select.go` (4 LIKE blocks),
`internal/parser/any_all_test.go` (`TestParseLikeFamilyAnyAll`),
`internal/executor/any_all_test.go` (`TestLikeFamilyAnyAllEvaluation`),
`docs/design/0134-0003-arrays-sql-divergence.md` (new),
`docs/design/README.md`, `.ralph/fix_plan.md`, `.ralph/deferral_ledger.md` (2
rows 2026-08-18).

**Next step:** M0134-0001/-0002/-0003 are all parked, so select the **next
unparked M0134 case**: M0134-0004 (`cluster.sql`) at `.ralph/fix_plan.md:6476`,
then 0005 onward (`failed` before `not-tried`). Start with
`scripts/pg-regress-runner.sh <case>` + a researcher classification pass before
briefing any implementer. **Never compare to a pre-2026-08-18 regress number** —
they predate the C19 harness fix (`-v HIDE_TABLEAM=on
-v HIDE_TOAST_COMPRESSION=on`); re-measure from scratch.

**Gates run:** `go test ./internal/parser/` PASS; `go test ./internal/executor/`
PASS; `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS; named
guards re-run by coordinator pre-commit PASS; pre-commit pgbench smoke PASS
(TPC-B 335 tps, simple update 632 tps, select-only 12.4k tps); regress `arrays`
3311→3251 + sentinel `char` PASS. No TPC-H/TPC-DS — parser-only.

**Delegation:** `tmp/ralph-handoffs/m0134-0003-s01-measure/` (researcher
`a78c083fd98e16874`, 2 rounds — the round-2 precision pass corrected its own
round-1 bucket-D framing); `tmp/ralph-handoffs/m0134-0003-s02-like-any-all/`
(implementer `a2dc9b3700c6a7f8b`, 1 round, DONE; tester `a7d28a25020e063e6`,
1 round, re-measure).

**In-flight:** none.
