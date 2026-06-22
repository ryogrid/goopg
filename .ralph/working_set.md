(idle — nothing in flight)

Last loop (#15, M0118-0001 COMPLETE): probe-first revealed ALL 19 SERIALIZABLE/SSI
anomaly specs already match PG 18.3 byte-for-byte (SSI 40001 detector M0104 +
pinned snapshot M0100). Promoted the whole group soft→`runIsoSpecStrict` with NO
engine change (design 0118-0025). M0118-0001 ticked [x]. Committed+pushed.

NEXT loop candidates (remaining M0118 groups):
- M0118-0002 predicate-lock granularity: predicate-gin/gist/hash/lock-hot-tuple,
  index-only-scan, index-only-bitmapscan, partial-index. PROBE FIRST — some
  (predicate-lock-hot-tuple, partial-index, index-only-scan) already have soft
  dedicated tests; run them to see which PASS for free. Likely some free wins.
- M0118-0007 eval-plan-qual: EPQ recheck over a JOIN returns (0 rows) where PG
  re-projects the updated row after concurrent UPDATE (~L1171 expected). Real
  EPQ-over-join executor work. HIGH cost.
- M0118-0005 fk-deadlock: FK-check KEY SHARE wait over-conflicts; root cause not
  pinned. HIGH cost. Also ri-trigger (user RI trigger), fk-partitioned (ATTACH
  PARTITION).
- M0118-0008 DDL group + M0118-0009 misc: mostly need real features (LISTEN/NOTIFY,
  2PC, pg_stat_*, ATTACH PARTITION, txn-scoped DDL table locks).

GOTCHAS: isolation specs run goopg as SUBPROCESS (debug→file). D-002 CSV is one
giant single-line row #13 (field 6 rationale must be COMMA-FREE; append before the
`,M0060-0004` field boundary). Promote via test-file soft→strict + D-002 narrative
note (not separate CSV rows). regen: gen-isolation-coverage + gen-oracle-inventory.
never gofmt -w. Untracked postgres/ + weekly_loc.* + requirements.txt are stray — leave them.
