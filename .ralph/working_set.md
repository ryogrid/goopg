# Working set — M0134-0005t landed (NO INHERIT parent ⇒ no coninhcount)

**Task:** M0134-0005 (`constraints.sql`) — **M0134-0005t LANDED**. Parent case stays
`[ ]`. Selected per the Current Priority banner (M0134 after M-NIGHTLY), and it was
the baton's ranked #1: fully pinned by the previous loop, no research pass needed.
M-NIGHTLY drained: `ci/logs/action-items.md` still at run `20260818-005518`,
**items: 0** — nothing to file.

**What landed (design §27):** three `if …NoInherit { continue }` guards, 9 lines in
`internal/executor/operators_ddl.go` — the `col.Inherited` parent-scan loop
(`:4013`), its `!col.Inherited` sibling (`:4043`), and `unmergeNotNullOnDetach`
(`:13504`, the Rule-#2 twin deferral from 0005q, folded in). PG oracle re-verified
from source, not taken from the prior loop's note: `MergeAttributes` calls
`RelationGetNotNullConstraints(relid, true, include_noinh=false)`
(`tablecmds.c:2757`) and that function skips `connoinherit` rows at the top of its
scan (`catalog/pg_constraint.c:834`) — the filtered list feeds BOTH `nncols`
(`:2759`) and `nnconstraints` (`:2952`), so such a constraint is invisible to
inheritance entirely. Measured **707 → 702 lines, hunks 31 → 31**.

**Three things worth not re-learning:**
- **One guard, two effects.** The `col.Inherited` loop both increments `inhCount`
  and assigns `name = pnc.Name`; guarding *before* the `EqualFold` match fixed the
  name half for free. Don't brief those as two sites.
- **Marker-line prediction held this time (5 vs ~4 predicted).** 0005s over-shot
  (24 vs 4) because its hunk went *fully* clean and took its context lines along;
  this hunk still carries a residual line, so it stayed at marker count. The rule:
  predict low when the hunk will still have survivors, high when it goes clean.
- **`:1854-1861` is the CORRECT half** — re-confirmed untouched. §26.6's guess that
  it was entangled with this bug is wrong; §26.9 already revised it. Do not touch.

**Gates run:** `go build ./...`; `go test ./internal/{executor,catalog}/`;
`go test -run 'TestPort_.*(NotNull|Constraint|Inherit|Partition)' ./internal/testport/`
(75.3s PASS, 12 existing guards + the new one);
`scripts/pg-regress-runner.sh constraints` (702/31);
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS (`internal/initdb`
cold at 436s — diff cache invalidation, not a regression; this scope EXCLUDES
`internal/testport`); `scripts/tpch-spotcheck.sh` PASS (**Q12=2, Q13=35** — Rule #1);
pgbench smoke via hook.

**Next step — pick from the remaining cluster.** Baseline is now **702 lines / 31
hunks** (never compare to a pre-2026-08-18 number). Ranked, carried forward:
1. The `ee.Pos` / `LINE N:` audit — do all ~20 `ee.Pos == 0` sites at once (36
   lines), not the two-site patch (6 lines); same review cost. **Now the cheapest.**
   Note `scripts/pg-regress-runner.sh` has its OWN bash `normalise_output` (script
   lines 250-266), NOT `NormalizeRegressOutput` in `framework/regress.go` — the
   framework one strips `LINE `/`^`, the gate one does not. Check which normaliser a
   payoff claim is about before trusting it.
2. `ATExecValidateConstraint` not recursing to descendants (`tablecmds.c:13213-13290`).
3. The CHECK half of `MergeConstraintsIntoExisting` (matched by **name**, not attnum).
4. `DROP CONSTRAINT … ONLY` not decrementing the child's `InhCount`/`IsLocal`
   (`notnull_tbl5_child`, diff `:486-487`).
5. NEW (ledgered this loop): `\d+`'s per-column *Nullable* blank for an inherited +
   PK-implied column — the last residual ATACC3 line; a describe-path bug.
6. Cosmetic: missing "merging column" NOTICE; suppressed inheritance NOTICEs;
   `regclass` ORDER BY sorting by non-OID.
All of 1-6 still need their own researcher pass per §21.1.

**Delegation:** `tmp/ralph-handoffs/m0134-0005t-noinherit/` — implementer
`a5dc101f1269faa8b` (1 round, DONE), tester `a9d71f600a6fbb00f` (1 round, both
gates PASS).

**In-flight:** none.
