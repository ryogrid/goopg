(idle — nothing in flight)

Last completed (loop #48): M0110-0004 — ported pg_resetwal/t/002_corrupted.pl
as TestPort_PgResetwal002Corrupted (CSV row RW-004 → port). Committed + pushed.

What this loop did: 002_corrupted.pl drives upstream pg_resetwal against a
deliberately corrupted global/pg_control on an init'd-but-never-started goopg
cluster — (1) all-zeroes → "broken or wrong version; ignoring it" + guessed
--dry-run dump; (2) 16-byte header restored + body zeroed → "invalid WAL
segment size (0 bytes); proceed with caution" (version-matches/CRC-fails code
path); (3) plain run refuses on guessed values; (4) --force rewrites. Generic
pg_resetwal logic; only goopg dependency is the pg_control header compat already
proven by RW-003. NO server start, so the earlier note pairing 002_corrupted
with the unclean-shutdown/--force deferral was wrong (corrected in CSV/design).
Files: internal/testport/pgresetwal_port_test.go (+TestPort_PgResetwal002Corrupted
+ header-comment fix), docs/test-port/postgres-oracle-port-status.{csv,md}
(RW-004 added, RW-002 trimmed), docs/design/0110-0004-pg-resetwal-tap-port.md
(loop #48 update + CSV rows + resume point), .ralph/fix_plan.md.
Gates: gofmt clean, go vet clean, all 3 TestPort_PgResetwal* PASS (3.3s).

RW-002 remainder still open (the last pg_resetwal deferral): (a) maximal-override
FINAL RESTART — --next-transaction-id advances NextXID past the bootstrap
pg_xact segment; CLog.MarkUnknownAsAborted (internal/mvcc/clog.go) walks the
whole xid range w/ per-step fsync during initdb.Open → startup looks hung.
Fix = PG-style StartupCLOG page-fill (separate WAL/MVCC task). (b) the
unclean-shutdown/--force branch of 001_basic.pl — goopg v0 has no crash state.

Other open M0110/M0095 tasks (blocked on big features): M0095-0003 (WAL
streaming), M0110-0001 (pg_dump catalog parity), M0110-0002 (pg_waldump index
AMs), M0110-0003 (pg_amcheck verify_heapam).

⚠️ TREE NOTE: a SEPARATE manual claude session left ~930 lines uncommitted WIP
across internal/{executor,planner,catalog,analyzer,parser,mvcc,server}/ + 2
untracked test files (executor/partition_gen_override_test.go,
parser/gen_override_test.go). NOT ralph's — do NOT commit/clobber. Stage your
own files explicitly (git add <paths>), never `git add -A`.
