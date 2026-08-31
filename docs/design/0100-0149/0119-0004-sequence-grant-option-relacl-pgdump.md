# 0119-0004 — Sequence GRANT … WITH GRANT OPTION (`pg_class.relacl`) round-trip in pg_dump (DU-002 slice 349)

Status: accepted
Milestone: M0119-0004 (pg_dump TAP port DU-002, slice 349)
Date: 2026-06-30

## Problem

Slice 333 made a plain sequence-level GRANT (`GRANT USAGE ON SEQUENCE s TO r`)
round-trip through pg_dump from the sequence's `pg_class.relacl`. The
grant-option variant — the sequence analogue of the table grant-option slice 332
and the function grant-option slice 348 — had no end-to-end coverage:

```
GRANT USAGE ON SEQUENCE public.gowgo_seq TO seq_wgo_role WITH GRANT OPTION
```

must restore with the grantee's re-grant ability intact, not as a plain
`GRANT USAGE …;`.

A sequence's `acldefault('s', 10)` grants `USAGE`/`SELECT`/`UPDATE` to the owner
only, so the grant with grant option materializes (PG 18.3):

```
{postgres=rwU/postgres,seq_wgo_role=U*/postgres}
```

The grantee's `USAGE` carries the grant-option `*`. pg_dump's `getTables` diffs
`relacl` against `acldefault('s', relowner)` and `buildACLCommands` routes the
grant-option privilege to its `privswgo` branch, emitting a single

```
GRANT USAGE ON SEQUENCE public.gowgo_seq TO seq_wgo_role WITH GRANT OPTION;
```

Unlike a function (whose sole privilege `EXECUTE` collapses to `ALL`), a sequence
exposes three privileges (`USAGE`/`SELECT`/`UPDATE`), so a single `USAGE` grant
stays `USAGE` rather than rendering as `ALL`. Verified byte-identical against
`./postgres/local_install` PG 18.3.

## Fix

None required — this slice is **test-only**. The grant-option plumbing landed
incrementally and is already object-type-agnostic for sequences:

- The catalog grant-option primitive `GrantTablePrivilegeWithGrantOption(relOID,
  role, priv, withGrantOption)` (table slice 332) OR-s a per-(role,priv)
  grant-option flag that `renderACLLetters` projects as a trailing `*`. Sequences
  share the OID-keyed `tableACLs` store.
- Slice 333 removed `sequence` from `nonTableGrantObjects` in
  `tryRecordTableGrant` (`internal/server/grant_ddl.go`), strips the leading
  `SEQUENCE` keyword, and records the grant under the sequence's OID via the same
  shared store; the recorder already parses the trailing `WITH GRANT OPTION` into
  `withGrantOption` and passes it to `GrantTablePrivilegeWithGrantOption` on the
  shared table/sequence code path.
- Slice 333 also added the sequence privilege order (`sequenceACLPrivOrder`:
  `SELECT 'r'`, `UPDATE 'w'`, `USAGE 'U'`) and owner baseline `rwU` to
  `relaclTextLockedSeq`, with the grant-option `*` logic shared via
  `relaclTextLockedFor`. Slice 333's `TestRelaclTextSequence` already unit-covers
  a sequence grant-option rendering (`UPDATE` with grant option → `rw*U`).

So `GRANT USAGE ON SEQUENCE … WITH GRANT OPTION` already records
`{postgres=rwU/postgres,seq_wgo_role=U*/postgres}` and pg_dump already re-emits
the `WITH GRANT OPTION` line. This slice adds the missing end-to-end fixture +
assertion that drives the real pg_dump binary against goopg and pins the exact
output, closing the sequence ACL grant-option gap with the same fidelity proof as
slices 332 (table) and 348 (function).

## Scope / non-goals

- Pinned case: `GRANT USAGE ON SEQUENCE … TO <role> WITH GRANT OPTION`,
  single-statement autocommit.
- Sequence `REVOKE GRANT OPTION FOR …` (clears only the flag, not the privilege)
  is still routed to the no-op path, as for tables/functions.
- Column-level (`pg_attribute.attacl`, heap re-sync) and database (`datacl`,
  `--create`-only) ACL projection remain open.
- Extended-protocol commit-time deferral stays architecturally entangled.
- Dump-fidelity only — goopg does not enforce sequence privileges.

## Blast radius

Zero — no production code changed. The slice adds one fixture (`CREATE SEQUENCE`
+ `CREATE ROLE` + `GRANT USAGE … WITH GRANT OPTION`) and one `strings.Contains`
assertion to the existing `TestPort_PgDumpConnectionSetup` cumulative guard.

## Tests / gates

- `internal/testport` `TestPort_PgDumpConnectionSetup` **DU-002 slice 349**
  asserts the exact `GRANT USAGE ON SEQUENCE public.gowgo_seq TO seq_wgo_role
  WITH GRANT OPTION;` line — byte-identical vs real pg_dump 18.3 (the test drives
  the real pg_dump binary against a live goopg server). PASS.
- `go build ./...` clean; pgbench smoke = pre-commit hook.
