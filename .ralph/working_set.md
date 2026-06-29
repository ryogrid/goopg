(idle — nothing in flight)

Last loop (#40): M0119-0004 **extended-statistics ownership round-trip in
pg_dump** (DU-002 slice 318) — LANDED. Design
`0119-0004-statistics-owner-roundtrip.md`. Test-only guard.

pg_dump emits object ownership from the TOC archive entry, not the createStmt:
`dumpStatisticsExt` sets `.owner = getRoleName(stxowner)` and `_printTocEntry`
renders `ALTER STATISTICS <nsp>.<name> OWNER TO <role>;` because "STATISTICS" is
in `_getObjectDescription`'s ALTER-able list (pg_backup_archiver.c:3799). Slices
314–317 dumped CREATE/COMMENT/SET STATISTICS but never asserted ownership. This
slice asserts the OWNER TO line for all four fixture stats objects
(statext_all/nd/expr/mix). No production code changed — ownership already
round-tripped via the `pg_statistic_ext.stxowner = 10` (bootstrap superuser)
virtual-row projection (catalog.go pgStatisticExt VirtualRows). The guard
protects against that cell regressing to NULL / a dangling OID (getRoleName would
fail → OWNER TO silently vanishes).

Gates: TestPort_PgDumpConnectionSetup PASS vs real pg_dump 18.3 (4.3s);
go build clean; pgbench smoke = pre-commit hook; ralph-state-guard OK.

NEXT loop — next pg_dump getter-battery gap (M0110-0001 / DU-002). Candidates:
- The pg_dump 002–010 catalog-view parity battery (further slices) — pick a real
  feature gap, not another assertion-only guard (statistics object dump is now
  fully covered: CREATE/COMMENT/expr/SET STATISTICS/OWNER TO all round-trip).
- Other M0119: M0119-0002 (CLOG store swap Part B — WAL/MVCC, needs race gate) /
  M0119-0005 (pg_waldump) / M0119-0006 (pg_amcheck). Extended-protocol
  commit-time deferral is architecturally entangled
  (see memory goopg_extended_protocol_autocommit).
