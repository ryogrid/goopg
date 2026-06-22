(idle — nothing in flight)

Loop #39: PROMOTED `cluster-conflict-partition` (4 perms) — M0118-0008 twelfth
promotion, partitioned sibling of `cluster-conflict`, byte-for-byte vs PG 18.3
with NO engine change (design 0118-0041).

Key insight: `ALTER TABLE … OWNER TO` does NOT recurse to partition children
(`tablecmds.c` `AT_ChangeOwner` "never recurses") so only the parent is owned by
the role. Upstream CLUSTER locks the PARENT AccessExclusive (waits behind a
concurrent `LOCK … IN SHARE UPDATE EXCLUSIVE MODE` on the parent then completes —
perms 1/2), then enumerates leaves WITHOUT locking them and skips every leaf the
role does not own (WARNING suppressed by `client_min_messages=ERROR`), so a
locked leaf is never touched and CLUSTER returns immediately (perms 3/4).
goopg's no-op `clusterOp.Next` locks only the named parent and never processes
leaves → output matches by both routes. Probe (zz_probe_test.go, deleted)
returned status=pass empty-diff immediately → free promotion.

Files: internal/testport/isolation_port_test.go (+TestPort_IsolationClusterConflictPartition);
docs/design/0118-0041 + README; port-status.csv D-002 rationale (comma-free!) +
regen port-status.md/target-inventory.md/upstream-isolation-coverage.md;
fix_plan + ledger.

Gates: new strict test PASS; conflict-family siblings (cluster/vacuum/truncate-
conflict) PASS; build+vet clean; pgbench smoke = pre-commit hook.

Next step: M0118-0008 tail. Closest remaining: `alter-table-{1,2,4}` (ADD/VALIDATE
CONSTRAINT lock semantics; INHERITS), partition ATTACH/DETACH specs
(detach-partition-concurrently-{1,2,3,4}, partition-concurrent-attach,
partition-drop-index-locking), `reindex-concurrently-toast`
(allow_system_table_mods GUC + TOAST reindex), `vacuum-no-cleanup-lock`
(reltuples accounting + cursor pin), `plpgsql-toast`. Probe-first each
(zz_probe_test.go → RunAndCompare .Diff) and rank by first-divergence cost.
