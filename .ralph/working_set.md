(idle — nothing in flight)

Last loop (#63) COMPLETE + committed: reconciled the stale per-spec isolation
inventory. Three specs were PROMOTED in prior loops (strict `runIsoSpecStrict`
tests, suite-level CSV flipped) but their rows in
`docs/test-port/postgres-oracle-target-inventory.csv` still read `failed`:
- `fk-partitioned-2` (design 0118-0121, M0118-0005 group close)
- `prepared-transactions` (design 0118-0112)
- `prepared-transactions-cic` (design 0118-0111)
Flipped all three failed→pass with proper promotion rationales; regenerated
`postgres-oracle-target-inventory.md` + `upstream-isolation-coverage.md` via
`go run ./cmd/gen-oracle-inventory` + `go run ./cmd/gen-isolation-coverage`.
Both sources now consistent: **116 pass / 5 failed** isolation specs.

REMAINING failed isolation specs (all genuinely Effort-L unbuilt subsystems —
need dedicated full-gate sessions; probe each with a throwaway zz_probe test to
rank by first-divergence cost before committing):
- `deadlock-parallel`   — M0118-0004; needs a lock-group abstraction goopg lacks.
- `index-only-bitmapscan` — M0118-0002; needs real Bitmap Heap Scan + BitmapOr +
  EXPLAIN DECLARE CURSOR plan rendering. Enablers already landed: INSERT…SELECT
  arity fix (0118-0038), VACUUM (TRUNCATE false) parse (0118-0108), cursor FETCH.
- `predicate-gin`       — M0118-0002; needs int[] type + GIN AM + AM-grain SIREAD.
- `predicate-gist`      — M0118-0002; needs point type + GiST AM + AM-grain SIREAD.
- `stats`              — M0118-0009; needs the pg_stat_* cumulative subsystem
  (pg_stat_force_next_flush + function-stats + stats_fetch_consistency + 2PC).

No code change this loop (docs/tracking only).
