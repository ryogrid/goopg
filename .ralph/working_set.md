(idle — nothing in flight)

Last landed: M0119-0004 DU-002 **slice 349** (sequence GRANT … WITH GRANT OPTION
`pg_class.relacl` round-trip in pg_dump). Test-only — NO engine change. The
sequence analogue of the table grant-option slice 332 + function grant-option
slice 348, closing the sequence ACL grant-option gap left by slice 333's plain
`GRANT USAGE ON SEQUENCE`. `GRANT USAGE ON SEQUENCE public.gowgo_seq TO
seq_wgo_role WITH GRANT OPTION` materializes relacl as
`{postgres=rwU/postgres,seq_wgo_role=U*/postgres}`; pg_dump emits `GRANT USAGE ON
SEQUENCE public.gowgo_seq TO seq_wgo_role WITH GRANT OPTION;` (USAGE stays USAGE,
not ALL — sequences expose 3 privileges; verified vs real PG 18.3). The
grant-option primitive `GrantTablePrivilegeWithGrantOption` + `renderACLLetters`
`*` projection are object-type-agnostic, and slice 333 already threads the parsed
WITH-GRANT-OPTION flag through the shared table/sequence recorder path, so the
slice only adds a fixture + assert to the cumulative
`TestPort_PgDumpConnectionSetup` guard. Gates: testport connsetup PASS; build
clean; pgbench smoke (pre-commit). Design
0119-0004-sequence-grant-option-relacl-pgdump.md.

NEXT M0119-0004 GRANT/ACL slices (remaining are deeper):
- column-level GRANT (`pg_attribute.attacl`, heap re-sync — the entangled one:
  pg_attribute is HEAP-backed, needs delete-old-rows + syncTableToCatalogHeap,
  which the server GRANT short-circuit cannot reach without executor routing).
- database GRANT (`datacl`, only dumped under `--create`; the harness runs
  pg_dump with `--no-sync` only, so untestable there as-is).
- TYPE/DOMAIN GRANT (`pg_type.typacl`, always NULL today; whole new ACL surface,
  needs getTypes/dumpACL objtype "TYPE" path — a fresh object class, likely a
  larger slice).
- Extended-protocol commit-time deferral stays architecturally entangled (see
  [[goopg_extended_protocol_autocommit]]).
