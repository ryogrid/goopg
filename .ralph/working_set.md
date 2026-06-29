(idle — nothing in flight)

Last landed: M0119-0004 DU-002 **slice 350** (sequence partial REVOKE
`pg_class.relacl` round-trip in pg_dump). Test-only — NO engine change. The
sequence analogue of table partial-REVOKE slice 338 / schema slice 339, and the
REVOKE counterpart of sequence GRANT slices 333/349. `GRANT USAGE, SELECT ON
SEQUENCE public.seqrev_seq TO seqrev_role` then `REVOKE SELECT …` leaves relacl
`{postgres=rwU/postgres,seqrev_role=U/postgres}`; pg_dump re-emits only `GRANT
USAGE ON SEQUENCE public.seqrev_seq TO seqrev_role;` (NOT the revoked SELECT;
verified vs real PG 18.3). The shared REVOKE recorder `tryRecordTableRevoke`
(slice 338) already clears sequence relacl bits (sequences share the OID-keyed
relation ACL store), so slice only adds fixture + assert (incl. negative
over-emit check) to `TestPort_PgDumpConnectionSetup`. Gates: connsetup PASS;
build clean; pgbench smoke (pre-commit). Design
0119-0004-sequence-revoke-relacl-pgdump.md.

NEXT M0119-0004 GRANT/ACL slices (remaining are deeper):
- column-level GRANT (`pg_attribute.attacl`, heap re-sync — entangled:
  pg_attribute is HEAP-backed, needs delete-old-rows + syncTableToCatalogHeap,
  which the server GRANT short-circuit cannot reach without executor routing).
- database GRANT (`datacl`, only dumped under `--create`; the harness runs
  pg_dump with `--no-sync` only, so untestable there as-is).
- TYPE/DOMAIN GRANT (`pg_type.typacl`, always NULL today; whole new ACL surface,
  needs getTypes/dumpACL objtype "TYPE" path — a fresh object class, likely a
  larger slice).
- Extended-protocol commit-time deferral stays architecturally entangled (see
  [[goopg_extended_protocol_autocommit]]).
