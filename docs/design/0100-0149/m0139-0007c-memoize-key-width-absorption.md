# M0139-0007c — port `get_expr_width` for Memoize's cache-key width term

Status: accepted

## Task

The follow-up M0139-0007b filed and deferred (`.ralph/fix_plan.md`
`M0139-0007c`, ledger row `m0139-0007b`): `costMemoizeRescan`'s per-key term
still stood in for PG's `get_expr_width` sum over `mpath->param_exprs`
(`postgres/src/backend/optimizer/path/costsize.c:2566-2567`) with goopg's own
`hashsize.EntryBytes(nkeys, 0)`, in BOTH currencies, because goopg had no
per-expression average-width statistic wired to this site. 0007b's own
resume-point candidate: "extend `pathAvgVarBytes`/`typeWidth`-style catalog
lookups to a bare `*ColumnRef` cache key, since `getMemoizePath`'s gates
already guarantee every key is one."

## Derivation

PG's `get_expr_width` (`costsize.c:6404`), for the only Node shape it can ever
see here (a `Var`, since every Memoize cache key is a bare column reference —
`getMemoizePath`'s gates 4/7/8, `joinpathsmemoize.go:200-228`):

```c
if (IsA(expr, Var)) {
    ... /* try RelOptInfo->attr_widths[varattno - min_attr] (ANALYZE's cached stawidth) */
    if (rel->attr_widths[ndx] > 0)
        return rel->attr_widths[ndx];
    /* else */
    return get_typavgwidth(var->vartype, var->vartypmod);
}
```

Two lookups already exist in this package for exactly this fallback order,
because every other width consumer here needs the same thing:

1. **The ANALYZEd average width.** PG's `rel->attr_widths[]` is populated from
   `pg_statistic.stawidth`, i.e. `ColumnStats.AvgWidth`
   (`internal/catalog/catalog.go:1882`). goopg's `computeColumnStats`
   (`internal/executor/operators_analyze.go:1237-1244,1296-1300`) already
   makes this the PG-equivalent quantity for both type shapes PG's own
   `is_varwidth` test distinguishes: a fixed-width column gets its `typLen`
   directly (matching PG's `analyze.c:2565-2569`, which never measures a
   by-value or fixed-length-by-reference type from the data), and a varlena
   column gets the measured mean payload width. `memoizeKeyNDistinct`
   (this file, existing) already reads the sibling statistic
   (`stats.NDistinctFrac` via `getVariableNumDistinct`) off the same
   `joinVarStats.stats` this task reads `.AvgWidth` from — one lookup site,
   two fields.
2. **The type-average fallback.** `typeWidth` (`relsize.go:291`) is already
   `get_typavgwidth`, ported and shared by every other width consumer in this
   package (Sort, Window, hash join sizing). No new port needed.

So `get_expr_width` reduces, for goopg's one admitted key shape, to "try
`ColumnStats.AvgWidth` via the same relation lookup `examineJoinVar` already
does for ndistinct; if there is no statistic or it is non-positive, fall back
to `typeWidth(cr.Type)`" — no new statistic, no new catalog surface, just the
two lookups this package already has, wired to the one caller that had not
used them yet.

## Code

- `internal/optimizer/joinpathsmemoize.go`:
  - `memoizeKeyWidths(s *searchCtx, innerPath *Path, outerRelids RelSet) float64`
    (new): mirrors `memoizeKeyNDistinct`'s loop over `innerPath.IndexClauses`
    exactly (same `memoizeKeyRelids` per-clause relid resolution), but sums
    `get_expr_width`'s per-key answer instead of multiplying ndistinct
    fractions.
  - `costMemoizeRescan` gained a `keyWidth float64` parameter. The per-key
    term now tracks the SAME currency choice as the tuple-bytes term beside
    it, not a separate flag: `keyWidth` (the ported sum) only when
    `pgMemoizeEntryBytesCostEnabled()` AND `pgRelationByteSize` itself
    succeeded; `hashsize.EntryBytes(nkeys, 0)` in every other case (switch
    off, or the PG byte-size substitution declined) — so neither term ever
    switches currency without the other switching with it.
  - `getMemoizePath`'s call site now passes
    `memoizeKeyWidths(s, innerPath, outer.Relids)`.

No new GUC: this completes an already-gated arm (`GOOPG_PG_MEMOIZE_ENTRY_BYTES_COST`,
M0139-0007b), it does not open a new one.

## Test

`internal/optimizer/joinpathsmemoize_test.go`:

- `TestMemoizeKeyWidthsUsesAnalyzedStatThenTypeWidth` — two subtests pin the
  fallback order directly against `memoizeKeyWidths`: an ANALYZEd outer column
  (`AvgWidth` set) must return that value; an un-analyzed one must return
  `typeWidth(catalog.Type{Name:"int4"})`.

`internal/optimizer/memoize_pgentrybytes_test.go`:

- `TestCostMemoizeRescanPGEntryBytesUsesKeyWidth` — `TestCostMemoizeRescanPGEntryBytesUsesWidthNotNCols`'s
  twin for the newly-wired term: with the PG currency on and every other input
  (including the tuple-bytes `width`) held constant, a wider `keyWidth` must
  price fewer cache entries into the same `workMem` than a narrower one, and
  the off-currency arm must show no such sensitivity — it never reads
  `keyWidth`.
- The five pre-existing `costMemoizeRescan` call sites (three in
  `joinpathsmemoize_test.go`, two pairs in `memoize_pgentrybytes_test.go`)
  updated for the new parameter, passed `0` — every one of them exercises
  either the off-currency arm (where `keyWidth` is provably unread, per
  `TestCostMemoizeRescanPGEntryBytesSwitchOffIsLegacy`) or compares two calls
  that both hold `keyWidth` constant, so the additive constant cancels and the
  pre-existing assertion is unaffected.

`go build ./...`, `go vet ./internal/optimizer/...` clean.
`go test ./internal/optimizer/...`: all pass (full package, no `-count=1`).

## No production diff to the default arm

`GOOPG_PG_MEMOIZE_ENTRY_BYTES_COST` still defaults unset/off (unchanged by
this task); with the switch off, `costMemoizeRescan` is byte-identical to
pre-task behaviour regardless of `keyWidth`'s value
(`TestCostMemoizeRescanPGEntryBytesSwitchOffIsLegacy`, unmodified by this
task). `scripts/tpch-spotcheck.sh` confirms canonical Q12=2/Q13=34 on the
fresh capped server (no ANALYZE, so every candidate takes
`getVariableNumDistinct`'s default arm regardless of this term either way).

## Why this does not reopen M0139-0007b's HOLD decision

0007b measured the whole currency arm byte-identical on both corpora because
`evictRatio` — the only path by which `estEntryBytes` reaches the final
cost — never activates: every observed Memoize candidate's `estCacheEntries`
swamps `ndistinct` under a 64 MB `work_mem` regardless of which byte
estimate produced `estCacheEntries`. Completing one more additive term inside
that same `estEntryBytes` sum does not change which regime the corpora
exercise; it only makes the (still-unreached) PG-currency arm more faithful
for the day a workload does reach it. No re-measurement is needed to reach
that conclusion — the arm remains **default-off, HOLD**, unchanged from
0007b's decision.

## Resolves

`.ralph/deferral_ledger.md` row `m0139-0007b`'s deferred scope (the per-key
`get_expr_width` term) is now landed; left `status: -` for M0119's own
consumption/triage pass to flip, per the ledger's own protocol ("M0119 sets
these values").

## Resume points

- Re-measure `GOOPG_PG_MEMOIZE_ENTRY_BYTES_COST` (both currencies, now fully
  absorbed) if a Memoize-relevant workload with a large per-key cached row
  count or a tight `work_mem` becomes part of the corpus — the regime
  `evictRatio` can actually move in, same as 0007b's own resume point.
