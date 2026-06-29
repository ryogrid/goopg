(idle — nothing in flight)

Last loop (#59): M0119-0004 **`GRANT` to a quoting-required role name
(`relacl` `putid`) round-trip in pg_dump** (DU-002 slice 336) — LANDED,
committed, pushed.

PG's `aclitemout`/`putid` double-quotes a grantee whose name has any char
outside `[A-Za-z0-9_]` (hyphen/space/multibyte; internal `"` doubled) in
`pg_class.relacl` → `"weird-role"=r/postgres`. pg_dump's `getid` relies on the
quotes to read the whole name, re-emits via `fmtId`. Slices 331–335 rendered
grantees RAW, so a hyphenated role emitted `weird-role=r/postgres` and pg_dump
mis-parsed at the hyphen.
- catalog.go: new `aclQuoteName` (putid emulation); `relaclTextLockedFor` wraps
  the grantee (after the publicPseudoRole→"" mapping). Identity for
  alnum/underscore + empty PUBLIC grantee → zero blast radius.
- No grant_ddl.go change (`splitGrantList` already trims quotes).
- Reserved-keyword name (`user`) is all-alnum → bare in aclitem, quoted
  client-side by pg_dump's fmtId → ALREADY round-trips (no goopg change; closes
  the loop-#53 "reserved-keyword-named-role quoting" item by analysis).

Files: internal/catalog/catalog.go, internal/catalog/relacl_test.go (new
TestRelaclTextQuotedGrantee + TestACLQuoteName),
internal/testport/pgdump_connsetup_test.go (slice-336 fixture+assert),
docs/design/0119-0004-grant-quoted-role-relacl-pgdump.md (+README 0119-0004am),
.ralph/fix_plan.md.

Gates: catalog units + slice-336 TestPort_PgDumpConnectionSetup (byte-identical
vs real pg_dump 18.3, 4.7s) PASS; catalog/server + truncate-conflict isolation
PASS; build clean. (pgbench smoke = pre-commit hook.)

NEXT loop — further M0119-0004 pg_dump GRANT slices: column-level GRANT
(`pg_attribute.attacl` — needs heap re-sync from an executor context the
server-side GRANT recorder lacks; the entangled one), mixed-case quoted-role
case preservation (`GrantTablePrivilege*` lower-cases the stored name),
REVOKE-of-default modelling. Database GRANT (`datacl`) is NOT testable in this
fixture (pg_dump runs `--no-sync postgres`, no `--create`). Extended-protocol
commit-time deferral stays architecturally entangled (auto-commit-per-statement).
