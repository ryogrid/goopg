(idle — nothing in flight)

Last loop: **M0127-P5.8** — LANDED, gates green, committed + pushed.
Facts the next loop must NOT re-derive:

1. `internal/planner/collapse.go` is new: `deconstructJointree` (the joinlist
   half of `deconstruct_recurse`, initsplan.c:1148-1452), `deconstructFromItem`,
   `combineJoinlists`, `soleItemOr`, types `joinlist`/`joinlistItem`,
   `collapseLimits`/`defaultCollapseLimits`, flag `GOOPG_PGSHAPED_COLLAPSE`
   (`pgShapedCollapseEnabled()`), and `deconstructRangeVars` for the JOIN-free
   parse shape.
2. **The finding.** Neither GUC is a search-size cap. `sub_members <= 1`
   collapses every single-baserel FROM item unconditionally, so a flat comma
   list is ONE problem at any width; `from_collapse_limit` governs merging
   MULTI-relation sub-joinlists, `join_collapse_limit` governs explicit JOINs.
   Reading them as a cap would re-introduce the greedy pre-reorder (03 §6's
   Q2 failure mode). Also: `=1` does NOT bite until the THIRD relation (a
   one-element side is unwrapped, initsplan.c:1428-1436).
3. `resolveContext.joinlist` is new (planner.go), set ONLY in `planFromClause`
   and `planFromRangeVars` — the two places where the FROM walk that numbers
   leaves IS the walk that appends bindings. A leaf's `rel` is therefore a
   direct `bindings`/`scans`/`relInfos` subscript
   (`TestJoinlistLeavesMatchBindings`). Nil in every non-FROM context.
4. Outer joins (LEFT/RIGHT/FULL) take upstream's FULL pin verbatim
   (`list_make1(list_make2(l,r))`) per 03 §4.4 — RIGHT included, goopg has no
   `reduce_outer_joins`. CROSS is upstream's JOIN_INNER.
5. **The 12-table bail-out (`bushy.go:99`) was deliberately NOT deleted.** The
   P5.8 TODO said to; 03 §7 says it dies WITH the bushy DP (P6.3) and §7 is
   right — it guards the OLD 3ⁿ subset-bitmask DP (3¹⁶ ≈ 43 M), still the
   production path. §7 and the `maxSearchRels` comment now say so explicitly.
   Do not "finish" P5.8 by deleting it.
6. DS05/PLAN not run and that is not a skip (same structural reason as
   P5.7-a/-b): `GOOPG_PGSHAPED_COLLAPSE` is OFF so production joinlists pin
   explicit JOINs exactly as today, and NOTHING reads `joinlist` under either
   flag setting.
7. 2 ledger rows: per-session collapse GUCs never reach `Plan` (no session
   arg — `SET join_collapse_limit=1` is still a no-op in a real session); no
   joinlist CONSUMER (`make_rel_from_joinlist` recursion) until P5.9.

Gates run: UNITS green (`/tmp/units-p58.log`, exit 0, zero FAIL lines);
planner+executor re-run uncached with `GOOPG_PGSHAPED_COLLAPSE=1` (one-off
`-count=1` env probe) both ok; commit-hook pgbench smoke. No orphaned servers.

Nightly triage 20260805-014309: unchanged, both items already filed under
M-NIGHTLY (fix_plan lines 1097/1203/1215), left unchecked per the banner.

Next step: per the banner (M0124 closed → M0125 → M0127), the head of open
M0127 work is **M0127-P5.9** — the S5 acceptance run (09 §3, run once with
collapse OFF then ON) + plan-shape ratchet baseline (§4) + estimate audit (§5),
then flip `GOOPG_PGSHAPED_DP` / retire `GOOPG_COST_DRIVEN_JOINORDER`, or record
the documented no-go. That slice must FIRST wire a consumer: `planSelect` →
recurse over `resolveContext.joinlist` → `preprocessLimit` →
`buildInitialRels` → `searchCtx.finalPath()`.

In-flight: none.
