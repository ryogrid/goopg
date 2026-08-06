(idle — nothing in flight)

Last loop: **M0125-0050 CLOSED — a CTE's runtime identity is its DECLARATION,
not its name.** The witness now returns 1,2 like PG (was 1,1).

1. **The task was entirely the choice of KEY**; the code change is ~6 lines.
   Both cheap candidates fail in OPPOSITE directions, and the filed suggestion
   was one of the wrong ones.
2. **Name over-shares** — the bug: `ctx.CTERowCache` keyed by lowercase name
   statement-wide, so two disjoint `WITH x` shared one buffer.
3. **Pointer / `declSeq` under-share** (the trap I nearly shipped): `planSelect`
   re-enters on the head operand of a set-op chain, so M0125-0040's
   grouping-sets rewrite plans its ONE synthetic `__gs_src_N` declaration
   TWICE. Measured on a 3-branch ROLLUP: 3 scans, 2 `*plannedCTE`, 2 declSeqs,
   1 declaration. Either key splits the buffer → the hoisted join runs twice →
   undoes -0040 on Q18/Q67, while still passing a name-only assertion.
4. **Landed key:** `CTEScan.DeclKey()` = declaring `CommonTableExpr` source
   offset + lowercased name (PG's `ctePlanId` analogue). Stable across
   replanning, distinct per declaration (all 3 `CommonTableExpr` producers
   checked), race-free — no counter, no lazily-mutated AST field.
5. EXPLAIN's `collectCTEHoist` moved to the same key, as -0049 said it must:
   grouping-sets source still ONE section; two same-named declarations now two
   `CTE x` sections — also what PG prints.

Files: `internal/planner/with.go` (`declPos` ×3 sites), `internal/planner/plan.go`
(`DeclKey`), `internal/executor/operators_cte_dml.go` (key), `explain_cte.go`
(`byName`→`byDecl`), comment sync in `context.go`/`executor.go`/
`cte_inline_pushdown.go`/`unnest.go`; tests in `with_compat_test.go` (+2),
`explain_cte_test.go` (+1), `groupingsets_share_test.go` (strengthened to
DeclKey — this is the §2.1 guard); `docs/design/0125-0050-cte-declaration-identity.md`
+ README index; fix_plan tick; 2 ledger rows.

Gates run: `internal/planner` + `internal/executor` PASS;
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS;
`scripts/tpch-spotcheck.sh` Q12=2/Q13=35 canonical PASS;
`scripts/tpcds-sf05-regression.sh plans` → `queries=99 same=99 changed=0`
(new baseline `plans-20260806-152448.txt`) — the bar the item stated, and the
real check that nothing un-shared; pgbench smoke via the commit hook.

NEXT LOOP (banner: M0124 closed → M0125 → M0127 → M-NIGHTLY → M0123).
Read `ci/logs/action-items.md` FIRST — it was still run `20260806-011323` this
loop (all 18 filed; nothing new). If a NEW nightly ran with `status: pass`, the
M0127 S7 gate is met and **M0127-P6.1 (delete fusion)** becomes selectable.
Otherwise the live M0125 choice is **`M0125-0048`** (the faithful `AGG_MIXED`
grouping-sets aggregate — fidelity, and it would RETIRE the `__gs_src_N` CTE
hoist this loop just hardened). -0031/-0032/-0033/-0041 stay
`[→ M0127: absorbed]` — never select them standalone. The two new ledger rows
(DML `MaterializedCTEs` name key; missing 42P19 top-level DML-CTE check) are
small and paired — the 42P19 check is the one to do first.

In-flight: none.
