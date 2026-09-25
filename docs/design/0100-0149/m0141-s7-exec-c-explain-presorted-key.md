# M0141-S7-exec-c — EXPLAIN rendering of `Presorted Key:`

Status: accepted (landed `c3f10a329`, 2026-09-17). Production change: `internal/executor/operators_explain.go` — the shared `sortKeyParts` helper plus the `*optimizer.IncrementalSort` case emitting `Sort Key:` and PG's undecorated `Presorted Key:` line.

Parent: M0141-S7 — see [m0141-s7-readjudicate-and-scope-incremental-sort.md](m0141-s7-readjudicate-and-scope-incremental-sort.md)

## Update 2026-09-17g — M0141-S7-exec-c LANDED

**PG oracle read (`explain.c:2583-2823`):** `show_incremental_sort_keys`
calls the SAME `show_sort_group_keys` a plain `Sort`/`MergeAppend` calls,
just with `nPresortedKeys = plan->nPresortedCols` instead of 0. Inside that
shared function, every key renders into the `Sort Key:` list exactly as
before (decorated with its DESC/COLLATE/NULLS suffix), but the function
ALSO captures each key's undecorated `exprstr` — the deparse output
*before* `show_sortorder_options` appends the direction/NULLS text — into a
second list, and if `nPresortedKeys > 0` emits that list's leading
`nPresortedKeys` entries as a second property, `Presorted Key:`
(`explain.c:2816-2822`). Two bits worth stressing: (1) `Presorted Key:`
carries NO direction/NULLS decoration even for a DESC key — PG's own
oracle strips it, this is not a goopg simplification; (2) both lines
render off the exact same per-key `keycols`/deparse loop, so goopg's
existing `*optimizer.Sort` key-formatting logic (the R65/S18 chase-through-
agg/Project machinery already landed for `Sort Key:`) is exactly the code
`Presorted Key:` needs too — a second independent implementation would risk
the "sibling paths must agree" trap for zero reason.

**Landed**, `internal/executor/operators_explain.go`:

- Extracted the `*optimizer.Sort` case's entire per-key loop (the R65 Arm-A
  agg chase, the Entry-(ii) Project chase, the S18 force-paren rule, the
  DESC/NULLS suffix logic) into a new shared helper, `sortKeyParts(child
  optimizer.Node, keys []optimizer.SortKey, reg *subPlanReg, qualify bool)
  (full, bare []string)`. `full` is the existing decorated per-key string
  (unchanged behaviour — captured `bare` before appending the suffix, so
  `Sort Key:`'s output is byte-identical to before this change); `bare` is
  the newly-captured pre-suffix string, goopg's analogue of PG's `exprstr`.
  The `*optimizer.Sort` case now just calls the helper and keeps its
  single `Sort Key:` line.
- New `case *optimizer.IncrementalSort:` right after it: calls the same
  `sortKeyParts` helper (same child/keys shape, so no new chase logic
  needed), emits `Sort Key:` from `full` exactly like `Sort`, then a second
  row `Presorted Key: ` + `strings.Join(bare[:p.PresortedCount], ", ")`.
  `PresortedCount` is always in `(0, len(Keys))` — enforced by
  `createIncrementalSortPlan`'s own panic check (exec-b) — so PG's `if
  (nPresortedKeys > 0)` guard always fires here; no conditional needed.
- The 3 remaining `operators_explain.go` sites named in the exec-c
  fix_plan entry (`resolveKeySource`, `childNodeOf`,
  `execParamOwnerChildren`) are confirmed still correct to leave declining
  — none is gated by a coverage test and none has a `*optimizer.
  IncrementalSort`-shaped reason to change; re-checked, not touched.

**Verification.** New test
`internal/executor/operators_incremental_sort_build_test.go`'s
`TestExplainIncrementalSortPresortedKey`: reuses exec-b's hand-built-plan
technique (`firstSort`/`replaceSort` over a real planner `Sort` for
`SELECT grp, v FROM inc_explain ORDER BY grp, v DESC`, swapped for
`IncrementalSort{PresortedCount: 1}`), wraps it in `&optimizer.
Explain{Options: {Costs off}}`, builds/opens/drains it directly (no SQL
`EXPLAIN` parse path exists for a hand-built node), and asserts (1) `Sort
Key: grp, v DESC` renders unchanged, (2) `Presorted Key: grp` renders (the
one presorted key, DESC-key excluded since `PresortedCount=1`), and (3) the
presorted line carries neither `DESC` nor the second key — pinning PG's
"bare exprstr, prefix only" rule against a false pass. Existing
`TestExplainEmitsSortKeyDetail` (plain `Sort`, no regression) and
`TestIncrementalSortReachesBothBuilders` (exec-b's own gate, unaffected by
an EXPLAIN-only change) re-verified passing.

Gates run: `go build ./...` clean; `go test ./internal/optimizer/...
./internal/executor/...` both green (`internal/executor` includes the new
test). `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` — only
failure is the same pre-existing, already-tracked `internal/parser`
`GroupedJoinUnaliased` AST-drift issue (optimizer/executor both `ok`,
confirmed untouched). `scripts/tpch-spotcheck.sh` SKIPPED (TPC-H bench
schema still not loaded, CLAUDE.md's M0142-0003k blocker, pre-existing) —
moot regardless: `GOOPG_INCREMENTAL_SORT` stays default-off, so no
production plan can reach this rendering path yet.

Resume point: **M0141-S7-exec-d** (deferred, ledger row already filed) —
`sortOp` feature parity (spill-to-disk, packed retention, ctid passthrough,
per-group `SortStat`) once the corpus measurement (running the 14 TPC-DS
witnesses with `GOOPG_INCREMENTAL_SORT=on`) shows a query actually needs
one of those. exec-a/b/c are now all landed, so that measurement is the
next open action in this milestone, though it is a measurement task, not
necessarily the next loop-sized code change — re-check the fix_plan banner
before selecting.
