(idle — nothing in flight)

Last loop: **M0127-PS6.2 DONE — E5's release gate, so STAGE E5 IS COMPLETE.**
The compiled ↔ interpreted sibling audit ran and found THREE divergences, none
of which any existing test could see. Carry the method, not just the result.

**The harness compares OUTCOMES, not values.** A panic, an error (code +
message + position) and a Datum are three points in one space; every corpus
entry is rendered to one string on both twins × every `SlotView` impl. That
shape is what PS6.1's own finding demanded — the twins had diverged in a
FAILURE mode (`ColumnRef` bounds), which a value diff is blind to.

1. **AND/OR short-circuited under different conditions — wrong ANSWERS on a
   join-residual seam.** `evalFastExpr` gated on `!left.IsNull()`;
   `evalAnd`/`evalOr` gate on `left.Kind == KindBool`. `BoolValue()` is
   `Int != 0` on ANY Kind, so a non-boolean left operand short-circuited AND
   WAS RETURNED as the AND/OR's value. For an ARENA-backed string `Datum.Int`
   is the mctx coordinate (`offset<<32|length`) — so which branch was taken
   depended on the arena offset. 619 diffs, one root cause.
2. **Two type-spelling decisions each existed twice** (`isFloatResultType` +
   an inline exact-match list; `overflowCodeForType` + an inline `switch`).
   The float pair is the transferable lesson: the compiled twin DIVERTS to
   `evalExprSlot` on that predicate, so when the two lists disagree the
   fallback lands in the branch it was diverted to avoid. One function each now.
3. **Every compiled error carried position 0** (`evalBinary`/`evalUnary`/
   `evalPgLSNBinary` got a literal 0), now compiled into `payload[4:8]`.
   **Invisible to every hand-built corpus** — `planner.BinaryOp.pos` is
   unexported, so both twins render 0 and agree for the wrong reason. Found
   only after adding a ninth corpus resolved from real SQL via
   `planner.ResolveIndexPredicate`. Generalise: a hand-built corpus cannot see
   anything only the planner can set.

Files: `exprnode.go` (short-circuit gate, pos in payload, `Pos` on the 22003),
`expr.go` (both inline lists replaced by the shared predicates),
`expr_sibling_parity_test.go` (new, 9 corpora), 05 §6.2 + 09 §1 +
IMPLEMENTATION-TODO (PS6.1 now `[x]`), fix_plan, 2 ledger rows.

Gates run: UNITS 0 FAIL; SPOT PASS (Q12=2, Q13=35); DS05 PASS=95 MISMATCH=0
CKMISMATCH=0 ERROR=0 TIMEOUT=0, plan shapes 99/99 identical
(`analysis/ps62/ds05.log`); BENCH 0 allocs, key 11.39→6.52, residual
149.8→130.3 ns/op; pgbench smoke via hook.

NEXT LOOP (banner wins — M0127 is #3 and current). Topmost unchecked is
**M0127-P6.1 — delete fusion** (`fused_hash_join.go` 707 lines, hook
`executor.go:160-163`, env vars, `IsCanonicalKeyEquality` orphan check). Note
the S7 precondition in the fix_plan order line: P6.1–P6.4 are gated on S5-ON
surviving a clean nightly cycle — check `ci/logs/action-items.md`'s newest run
before selecting, and if that has not happened, say so and pick per the banner.

Nightly triage: `ci/logs/action-items.md` still run 20260806-011323, 18 items,
all subjects already filed under M-NIGHTLY. Nothing new.

In-flight: none.
