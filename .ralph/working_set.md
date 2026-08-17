# Working set — M0134-0001 S17 LANDED

**Task:** M0134-0001 (`aggregates.sql`), slice **S17 — the `"Parallel "` EXPLAIN
node-label prefix**. Selected per the Current Priority banner (M-NIGHTLY drained:
`ci/logs/action-items.md` still run `20260817-011734`, all 6 `[x]`; nothing new
to file).

**Fix:** a real per-node `Parallel bool` on `SeqScan`/`BitmapHeapScan`, stamped
once at Gather-construction time by `stampParallelScan` (`parallel.go`), rendered
by `describePlan` **and** `describePlanVerbose`.

**Why not a render-time walk:** PG's `parallel_aware` is per-PATH-CHOICE
(`pathnode.c:996`/`:1115`, gated `parallel_workers > 0`), not per-tree-position —
PG admits a single-copy `Gather` over a non-partial subtree whose scan takes NO
prefix. `explain.c:1630-1631` renders it generically for any node kind.

**The twin the brief got wrong — worth carrying:** research concluded
`describePlan` was the single label emitter and `describePlanVerbose` needed no
change. False: its `*SeqScan` case has three independent `return`s and does not
fall through, so `EXPLAIN VERBOSE` would have kept rendering a bare `Seq Scan`.
Shipping that would have *created* a divergence. Caught by the implementer, fixed
under coordinator ruling; both cases now carry reciprocal sibling-pair comments.
Same failure mode as S11's two walkers — the standing sibling-paths rule earned
its keep again, and against a research report this time, not just against code.

**Measurement:** `aggregates` **999 → 981 lines, 29 → 28 hunks**; the
`array_dims(array_agg(s))` block gone entirely — first slice this milestone where
the predicted win landed exactly as scoped. Class confirmed beyond the case:
`select_distinct` **304 → 301**.

**Files:** `internal/optimizer/{plan.go,parallel.go,parallel_test.go}`,
`internal/executor/{operators_explain.go,parallel_label_test.go}`,
`docs/design/0134-0001-p2-explain-format.md` (S17 section) + README row.

**Gates run:** build + vet PASS; `go test ./internal/optimizer/ ./internal/executor/`
PASS; UNITS suite PASS; regress `aggregates` 981/28, sentinels byte-identical
(`functional_deps` 56, `groupingsets` 2373); `scripts/tpch-spotcheck.sh` PASS
Q12=2/Q13=35; pgbench smoke PASS via hook.

**Deferral ledger:** S16's `"Parallel "` row flipped `resolved`; 3 new rows
2026-08-17 — `BitmapHeapScan` has no `describePlan` case at all (its correctly
stamped flag has no reader), the non-text `"Parallel Aware"` JSON property
(`explain.c:1652`, emitted on EVERY node ⇒ JSON-format-wide), and the absent
parallel index-scan eligibility that makes `balk`'s `Parallel Index Only Scan`
unreachable.

**Next step:** continue **M0134-0001**. Remaining buckets at 28 hunks:
deparser/C11c 8, S6 min/max-InitPlan 5, isolated bugs 4, qualification 3, plus
the `string_agg` delimiter coercion (closes hunk 18, already ledgered and
self-contained — cheapest next).

**Delegation:** `tmp/ralph-handoffs/m0134-0001-s17-parallel-label/` — `brief.md`
(researcher, 1 round), `impl-brief.md` (implementer, 2 rounds; round 2 =
coordinator rulings on the VERBOSE twin + the relaxed pointer-identity
assertion), `gate-brief.md` (tester, 1 round).

**In-flight:** none.
