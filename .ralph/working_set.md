(idle — nothing in flight)

M0119-0004-ACLHEAP is COMPLETE (loop #89). Both heap-backed user-facing ACL columns now
round-trip through real pg_dump 18.3: typacl (TYPE/DOMAIN, loop #87) + attacl (column GRANT,
loop #89). datacl is permanently deferred (pg_database heap + pg_dump --create-only →
untestable under the --no-create connsetup harness; ledger). The fix_plan box is [x].

Next loop: pick a fresh M0119 item from the fix_plan (e.g. M0119-0005 pg_waldump server tier,
M0119-0006 pg_amcheck server tier) or the next DU-002 pg_dump catalog-parity slice via the
self-promoting TestPort_PgDumpConnectionSetup guard.
