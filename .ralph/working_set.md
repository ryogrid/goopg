(idle — nothing in flight)

## Loop summary (2026-07-12, loop #65)

**Nightly triage:** action-items batch `20260711-011536` (same as #58–#64) —
all 3 AI items already `[x]` in M-NIGHTLY. No new nightly work.

**Task — M0122-0007 follow-up: surface GEQO + planner-tuning GUCs in
pg_settings.** Last loop registered the 10 GUCs in the config registry (SET/SHOW
work) but deferred the pg_settings half: `pg_catalog.pg_settings` is a
*separately* hand-curated literal list (`pgSettings.VirtualRows`,
internal/catalog/catalog.go ~L10010), NOT registry-derived, so
`SELECT * FROM pg_settings WHERE name='geqo_threshold'` returned nothing while
`SHOW geqo_threshold` worked. Ledger row 722 named this exact resume point.

Landed:
- internal/catalog/catalog.go: added 10 rows (geqo/geqo_threshold/geqo_effort/
  geqo_pool_size/geqo_generations/geqo_selection_bias/geqo_seed +
  constraint_exclusion/cursor_tuple_fraction/recursive_worktable_factor) to the
  pg_settings VirtualRows literal. name/setting/category/vartype/min/max/enumvals/
  boot_val byte-for-byte from guc_tables.c (categories "Query Tuning / Genetic
  Query Optimizer" + "Query Tuning / Other Planner Options"; short_desc/extra_desc
  from same gettext_noop literals; constraint_exclusion enumvals {partition,on,off}).
  List re-sorted by name after append → name-sort contract preserved.
- internal/catalog/catalog_test.go: TestPgSettingsPlannerTuningGUCs.
- docs/design/root-0004-configuration-and-guc.md new "pg_settings surfacing"
  subsection; fix_plan + deferral-ledger (row appended) + unimplemented_feat.json
  M0097-0069 code_audit updated in place.

Still deferred (ledgered): behavioral no-op (planner reads none of the 10) +
pg_settings remains a hand-curated subset, not registry-derived.

Gates: go build ./... clean; go vet + go test ./internal/catalog/...
./internal/config/... PASS; make ralph-state-guard consistent (auto-repaired
prev-loop clean-exit marker). Catalog virtual-rows only — no planner/executor/
codec logic; tpch-spotcheck not implicated; pgbench smoke runs via pre-commit hook.

Next-loop candidates (open, bounded-ish):
- pg_get_expr stub entry is STALE — it's actually a functional pass-through.
- ALTER DOMAIN SET SCHEMA: LARGE — domains are hardcoded namespace=public (2200)
  everywhere; no Domain.Schema field; needs schema-qualified domain infra.
- RANGE window value-offset (M0122-0004): already implemented (inRange/
  frameBoundsRange handle numeric/float/interval); only interval-sign edge deferred.
- scram_iterations GUC: already fully wired (resolveScramIterations).

In-flight: none
