(idle — nothing in flight)

Last landed: M0119-0004 DU-002 **slice 351** (table `GRANT ALL` collapse,
`pg_class.relacl`, round-trip in pg_dump). Test-only — NO engine change. The
table analogue of the function (slice 345) and sequence (slice 333) GRANT ALL
collapses. `GRANT ALL ON TABLE public.grantall_t TO grantall_role` →
relacl `{postgres=arwdDxtm/postgres,grantall_role=arwdDxtm/postgres}`; pg_dump
recognises the grantee's full set == ACL_ALL_RIGHTS_RELATION and re-emits the
single `GRANT ALL ON TABLE public.grantall_t TO grantall_role;` (verified vs real
PG 18.3 — relacl + ACL line captured). No production code: `parseGrantPrivileges`
already expands ALL→allTablePrivileges and `renderACLLetters` emits "arwdDxtm".
Slice adds fixture + positive/negative assert (negative guards a dropped bit →
explicit `GRANT INSERT, SELECT …` list instead of `ALL`) to
`TestPort_PgDumpConnectionSetup`. Gates: connsetup PASS; build clean; pgbench
smoke (pre-commit). Design 0119-0004-table-grant-all-collapse-relacl-pgdump.md.

NEXT M0119-0004 GRANT/ACL slices (remaining are deeper):
- multi-grantee single table (two non-owner relacl entries → two GRANT lines;
  tests deterministic sort.Strings ordering — likely no engine change, untested).
- column-level GRANT (`pg_attribute.attacl`, heap re-sync — entangled).
- database GRANT (`datacl`, only under `--create`; harness runs --no-sync only).
- TYPE/DOMAIN GRANT (`pg_type.typacl`, always NULL today; new ACL surface).
- Extended-protocol commit-time deferral stays architecturally entangled (see
  [[goopg_extended_protocol_autocommit]]).
