(idle — nothing in flight)

Last loop (#16, M0118-0002 PARTIAL): probe-first ranked all 7 predicate-lock
specs. 3 already byte-identical vs PG 18.3 → promoted soft→`runIsoSpecStrict`
with NO engine change (predicate-lock-hot-tuple, partial-index, index-only-scan;
design 0118-0026). 4 deferred (ledger). Committed+pushed.

NEXT loop candidates (remaining M0118 groups):
- M0118-0002 remaining (deferred): predicate-hash is the cleanest next target —
  goopg OVER-detects 40001 where PG commits (coarse relation-grain SIREAD vs PG's
  finer hash-index page/tuple predicate locking — the canonical granularity gap).
  predicate-gin/gist/index-only-bitmapscan need missing index AMs (GIN/GiST/bitmap)
  — much higher cost.
- M0118-0007 eval-plan-qual: EPQ recheck over a JOIN returns (0 rows) where PG
  re-projects the updated row after concurrent UPDATE (~L1171 expected). Real
  EPQ-over-join executor work. HIGH cost.
- M0118-0005 fk-deadlock: FK-check KEY SHARE wait over-conflicts (INSERT-into-child
  blocks where PG proceeds); ri-trigger (user RI trigger); fk-partitioned (ATTACH
  PARTITION). HIGH cost.
- M0118-0008 DDL group + M0118-0009 misc: mostly need real features (LISTEN/NOTIFY,
  2PC, pg_stat_*, ATTACH PARTITION, txn-scoped DDL table locks).

GOTCHAS: isolation specs run goopg as SUBPROCESS (debug→file). D-002 CSV is one
giant single-line row #13 (field 6 rationale must be COMMA-FREE; append before the
`,M0060-0004` field boundary). Promote via test-file soft→strict + D-002 narrative
note (not separate CSV rows). regen: gen-isolation-coverage + gen-oracle-inventory.
PROBE pattern: throwaway zz_probe_test.go in internal/testport importing
internal/testutil/cluster (NOT internal/testport/cluster) + framework; run
RunAndCompare, log status+Diff; delete after. never gofmt -w. Untracked postgres/ +
weekly_loc.* + requirements.txt are stray — leave them.
