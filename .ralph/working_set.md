# Working set — M0134-0005: `reg*[]` cast landed; next slice is PREPARE param coercion

**Task:** M0134-0005 (`constraints.sql`) — the **`reg*[]` array cast** landed
2026-08-18; the case stays `[ ]`. Selected per the Current Priority banner (M0134
next after M-NIGHTLY). M-NIGHTLY drained: `ci/logs/action-items.md` still at run
`20260818-005518`, **items: 0** — nothing to file.

**Root cause was TWO stacked causes — do not re-bisect.** A six-step bisect
**cleared** `pg_constraint` contype='n' population, the `convalidated`/
`conislocal`/`coninhcount` columns, `= ANY`, plain `int[]` prepared params, and
the `COLLATE "C"` sort. **Cause A (FIXED):** `evalCast` had **no case for any
reg\*-array type** → fell through to the terminal `return d, nil // pass-through
for unknown types`, so `'{tbl}'::regclass[]` stayed raw text (`pg_typeof` →
`text`) and every OID comparison silently evaluated **false**. New arm covers all
8 reg\*[] types, shaped like `case "name[]":`, resolving via `regIdentifierInput`;
misses now raise 42P01/42704. Rule-#2 sibling check **negative** (encode twin
`codec_array.go:encodeArrayElem` already correct). Forced adjacent fix:
`evalExprSlot`'s reverse-direction `TargetType == "regtype[]"` oidvector case
intercepted brace literals — now requires the string not to start with `{`.

**Cause B (NOT fixed) is why the metric moved zero.** `internal/postmaster/
dispatch.go` never applies `prepDef.paramTypes` as a coercion (arity check +
`execParamTypeIncompatible` only), so `EXECUTE get_nnconstraint_info('{…}')` is
still `(0 rows)` while the inline `ANY('{tbl}'::regclass[])` form now returns the
row. Generic, not reg\*-specific (scalar `regclass` mis-binds identically).

**Measurement:** `constraints` 1411 → **1411** lines, hunks 33 → **33**, all 15
`get_nnconstraint_info` mentions still in open hunks. That is a **stacked root
cause** — a third outcome shape beside Bucket 1's *unmasking* and Bucket 6's
*bucket interference*. A flat number here does NOT mean the slice did nothing.
**Never compare to a pre-2026-08-18 `constraints` number** (pre-C19 harness).

**Files:** `internal/executor/expr.go` (`evalCast` reg\*[] arm, `evalExprSlot`
`regtype[]` guard), `internal/executor/reg_array_cast_test.go` (new),
`docs/design/0134-0005-constraints-sql-divergence.md` (§7 new, §8 next slice),
`docs/design/README.md`, `.ralph/fix_plan.md` (new sub-item M0134-0005a),
`.ralph/deferral_ledger.md` (2 rows).

**Next step:** brief **M0134-0005a** — apply a PREPARE's declared parameter types
as a real coercion at EXECUTE time in `internal/postmaster/dispatch.go`, mirroring
`postgres/src/backend/commands/prepare.c:EvaluateParams` (`coerce_to_target_type`);
probe numeric/interval/date for the same mis-binding; verify with `PREPARE
gi(regclass[]) … EXECUTE gi('{notnull_tbl1}')` → 1 row, then re-measure. After
that: Bucket 4 (deferred UNIQUE — milestone-sized, research first). Bucket 5
(GiST `circle_ops`) is a milestone. **Do not brief Bucket 7** (root cause unpinned).

**Gates run:** `TestRegArrayCastResolvesElements` FAIL-pre (8/9) / PASS-post (9/9);
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS (7m49s; warm
except cold `cmd/goopg` + `internal/initdb`); `scripts/tpch-spotcheck.sh` PASS
(Q12=2, Q13=35 — executor change, Rule #1); `scripts/pg-regress-runner.sh
constraints` re-measured 1411/33. No TPC-DS (cast-only, no row-shape impact).

**Delegation:** `tmp/ralph-handoffs/m0134-0005-s06-nnconstraint-info-zero-rows/`
(researcher `aba408b63734bed9c`, 1 round — pinned cause A, wrongly cleared the
PREPARE path); `tmp/ralph-handoffs/m0134-0005-s07-regclass-array-cast/`
(implementer `a2a9a205dbc6e1cb1`, 1 round NEEDS-DECISION → coordinator accepted
option (a); tester `ae0f7459b162fe262`, 3 gates PASS).

**In-flight:** none.
