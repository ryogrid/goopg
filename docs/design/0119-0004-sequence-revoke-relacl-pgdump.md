# M0119-0004 DU-002 slice 350 — sequence partial REVOKE (`pg_class.relacl`) round-trip in pg_dump

## Summary

A sequence-level `GRANT` followed by a partial `REVOKE` must round-trip through
`pg_dump`. This is the sequence analogue of the table partial-REVOKE slice 338
and the schema partial-REVOKE slice 339.

A sequence exposes three distinct privileges (`USAGE`/`SELECT`/`UPDATE`), so:

```sql
GRANT USAGE, SELECT ON SEQUENCE public.seqrev_seq TO seqrev_role;
REVOKE SELECT          ON SEQUENCE public.seqrev_seq FROM seqrev_role;
```

clears only the `SELECT` bit, leaving `pg_class.relacl` as
`{postgres=rwU/postgres,seqrev_role=U/postgres}` (the lone `USAGE`).

`pg_dump`'s `getTables` diffs that against `acldefault('s', 10) =
{postgres=rwU/postgres}` and re-emits only:

```sql
GRANT USAGE ON SEQUENCE public.seqrev_seq TO seqrev_role;
```

— NOT the revoked `SELECT`. Verified byte-identical to real pg_dump 18.3.

## Why no engine change

The shared REVOKE recorder (`tryRecordTableRevoke`) already removes the named
bits from a sequence's relacl — sequences share the relation ACL store with
tables, and slice 338 wired the bit-clearing path. The grant/diff/render
pipeline is object-type-agnostic for the `s` (sequence) relkind. This slice is
therefore test-only: it adds a fixture + assert to the cumulative
`TestPort_PgDumpConnectionSetup` guard, protecting against a regression that
would let the revoked `SELECT` survive in relacl and over-emit
`GRANT SELECT, USAGE ON SEQUENCE …`.

## Oracle

Verified against `postgres/local_install` PG 18.3: after the GRANT/REVOKE pair
above, `pg_class.relacl` = `{postgres=rwU/postgres,seqrev_role=U/postgres}` and
`pg_dump --no-sync` emits exactly `GRANT USAGE ON SEQUENCE public.seqrev_seq TO
seqrev_role;`.

## Tests

- `go test -run TestPort_PgDumpConnectionSetup ./internal/testport/` — PASS.

## Related

- Slice 338 — table partial REVOKE (`relacl`).
- Slice 339 — schema partial REVOKE (`nspacl`).
- Slice 333 — sequence `GRANT USAGE` (`relacl`).
- Slice 349 — sequence `GRANT … WITH GRANT OPTION` (`relacl`).
