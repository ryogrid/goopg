(idle — nothing in flight)

Last loop (#14, M0118-0007 PARTIAL): promoted `drop-index-concurrently-1` to
pass-required (strict) — already byte-identical to PG 18.3, NO engine change
(design 0118-0024). Probe-first ruled out cheap wins in M0118-0008 (all defer:
need DDL table locks / partition / dollar-quote-in-DO features). Committed+pushed.

NEXT loop candidates (engine work, no more free promotions found):
- M0118-0007 eval-plan-qual: EPQ recheck over a JOIN returns (0 rows) where PG
  re-projects the updated row after a concurrent UPDATE (~L1171 expected). Make
  the EPQ recheck re-evaluate the full join plan against the updated tuple.
- M0118-0005 fk-deadlock: the FK-check KEY SHARE on the WAIT path over-conflicts —
  perm1 `s1i s1u s1c s2i` shows s2i blocking after s1 committed its NO-KEY-UPDATE,
  where PG proceeds. Root cause NOT yet pinned (scanRelForFKMatch's clean-match
  gate at operators_fk.go:729 should already return true for a committed/inactive
  updater — needs subprocess debug-to-file to find why s2i still waits). HIGH cost.
- M0118-0008 DDL group: create-trigger & sequence-ddl both need DDL to take a
  txn-scoped table lock (CREATE TRIGGER = ShareRowExclusive; sequence DDL lock)
  that conflicts with concurrent DML — the timeouts work added tableLockMgr
  txn-scoped ACCESS SHARE/EXCLUSIVE infra (0118-0018) which could be extended here.

GOTCHAS: isolation specs run goopg as SUBPROCESS (debug→file). D-002 CSV is one
giant single-line row #13; individual specs promote via test-file soft→strict +
D-002 narrative note (not separate CSV rows). never gofmt -w. Untracked postgres/
+ weekly_loc.* + requirements.txt are stray — leave them.
