(idle — nothing in flight)

Last loop (#60): M0119-0004 **`GRANT` to a case-significant (mixed-case quoted)
role name round-trips through pg_dump** (DU-002 slice 337) — LANDED, ready to
commit + push.

PG role names are case-significant when double-quoted; `aclitemout` renders the
role's TRUE name in `relacl` (`MixedCase=r/postgres`, bare because all-alnum),
and pg_dump re-quotes via `fmtId` → `TO "MixedCase"`. goopg's ACL store keys
privileges by the lower-cased name (case-insensitive lookups), so it rendered
`mixedcase` → pg_dump emitted `TO mixedcase` (nonexistent role). This was the
slice-336 deferred limitation.
- catalog.go: new `roleACLDisplay map[string]string` (lowerRole→original
  spelling); populated in `GrantTablePrivilegeWithGrantOption` (only when spelling
  differs from lower-case); `relaclTextLockedFor` resolves it after PUBLIC→""
  mapping, before `aclQuoteName`. Mixed-case + special-char composes.
- No grant_ddl.go change (`handleQuery` passes raw original-case `matchable`;
  `splitGrantList` trims quotes → store gets `MixedCase`).
- Zero blast radius: display map consulted only in rendering; lookups read the
  lower-cased key (all case variants resolve).

Files: internal/catalog/catalog.go, internal/catalog/relacl_test.go (new
TestRelaclTextMixedCaseGrantee), internal/testport/pgdump_connsetup_test.go
(slice-337 fixture grant_mc + assert),
docs/design/0119-0004-grant-mixed-case-role-relacl-pgdump.md (+README 0119-0004an),
.ralph/fix_plan.md.

Gates: catalog units + slice-337 TestPort_PgDumpConnectionSetup (byte-identical
vs real pg_dump 18.3, 5.2s) PASS; catalog/server + truncate-conflict isolation
PASS; build clean. (pgbench smoke = pre-commit hook.)

NEXT loop — further M0119-0004 pg_dump GRANT slices: column-level GRANT
(`pg_attribute.attacl` — needs heap re-sync from an executor context the
server-side GRANT recorder lacks; the entangled one), REVOKE-of-default
modelling. Database GRANT (`datacl`) is NOT testable in this fixture (pg_dump
runs `--no-sync postgres`, no `--create`). Extended-protocol commit-time deferral
stays architecturally entangled (auto-commit-per-statement).
