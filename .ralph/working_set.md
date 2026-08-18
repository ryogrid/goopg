# Working set — M0134-0005ab LANDED (INHERITS column merge)

**Task:** M0134-0005 / sub-item **M0134-0005ab** — LANDED, item ticked. Selected
per the Current Priority banner (M-NIGHTLY had no new item; M0134 is next).

**Nightly triage:** `ci/logs/action-items.md` still shows run `20260819-011823`,
items: 1 — the SAME AI-20260819-011823-001 fixed two loops ago. Nothing new to
file. The next nightly run should drop it.

**What landed:** goopg implemented PG's column-merge rule ONLY in the `@@LIKE:`
branch of `execCreateTable` (`internal/executor/operators_ddl.go`). The plain
explicit-column path (`addCol`) never consulted `inheritedColNames` and just
appended — and nothing de-duplicates before `catalog.InMemory.CreateTable`.
Three symptoms, ONE cause:
1. no `merging column "%s" with inherited definition` NOTICE on plain INHERITS;
2. the column was **created twice** (verified live pre-fix: 4 `pg_attribute`
   rows for 2 columns) — invisible in the diff text, the real defect;
3. the multi-parent NOTICE double-fired on a 3-level chain — that site was NOT
   buggy, it iterated a middle table already holding `i` twice. Fixing `addCol`
   corrected it with **zero changes there** (the brief forbade touching it in
   round 1 precisely to test this).
Fix: one shared `mergeExplicitColumnIntoInherited` called by BOTH paths, LIKE's
inline copy deleted. Constraint identity needed a new `explicitNotNullLocal`
signal (from `NotNullExplicit`) because `col.Inherited` is false whenever the
child redeclares at all — that alone cannot separate "retyped only" (merge) from
"wrote NOT NULL itself" (`child2_t`, stays local).

**Measurement:** constraints **460 → 406 lines, 23 → 21 hunks**. Never compare to
a pre-2026-08-19 number.

**Worth not re-learning:** when a diff shows a doubled NOTICE, suspect doubled
STATE upstream before editing the emit site. And the `child2_t` test was the
over-merge guard — a fix that relaxed it would have swapped one wrong answer for
another.

**Gates run:** `go build ./...` PASS; `go test ./internal/executor/
./internal/catalog/` PASS; `scripts/pg-regress-runner.sh --verbose constraints`
460/23 → 406/21; `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
PASS; the 3 new guards re-run by the coordinator PASS; pgbench smoke via hook.

**Next step:** continue M0134-0005 at the **406/21** baseline. Take a FRESH
census — the previous one was taken at 460/23 and this slice removed 4 hunks, so
its ranking below is now partly stale. Standing candidates from that census:
1. **chained-cast column label** — `targetMeta`
   (`internal/optimizer/planner.go:11423-11449`) has no `*CastExpr` operand arm.
   Needs PG's strength semantics, not a naive recurse (`NULL::int::text` must
   stay `text`). Ledgered; separable `*CollateExpr` gap alongside.
2. **`SET NOT NULL` cascade does not propagate `convalidated=true`** to an
   already-merged descendant NOT NULL — freshly ledgered this loop, and it
   survives into the remaining 21 hunks.
3. `pg_get_partition_constraintdef` — 1 hunk but breaks `\d+` on ANY partition
   table; from-scratch builder, higher risk.
Runner-up: partitioned-parent `SET NOT NULL` never scans partitions
(`operators_ddl.go` ~10070, `forEachLiveRow` on a parent with no local storage).

**Delegation:** `tmp/ralph-handoffs/m0134-0005ab-{research,impl}/`
(researcher `a2a3163d77f4316ac` DONE 1 round; implementer `a8033ba1654072cdf`
DONE 1 round, one in-goal deviation: the `explicitNotNullLocal` signal, which
criterion 3 could not be met without).

**In-flight:** none.
