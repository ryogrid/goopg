(idle — nothing in flight)

M0119-0006 54th slice landed — binary `COPY` of `oid`/`regproc`/`xid`/`xid8` and
`uuid`. The default's `KindInt` escape shipped EIGHT bytes where `oidsend`/
`regprocsend`/`xidsend` are `pq_sendint32`; `uuid` shipped 36 chars of TEXT where
`uuid_send` is `pq_sendbytes(…,16)`; and the DECODE half handed all five back as
raw-bytes string Datums, so an `oid` from binary COPY did not compare/index like
the same `oid` from INSERT. Coercion + upstream's 22003 range rule (`uint32in_subr`)
extracted into a shared `pgUnsignedIDFromDatum`. Item stays UNCHECKED (standing
slice-by-slice cluster); 1 ledger row resolved, 3 filed; design
`0119-0006-copy-binary-oid-family.md` + README index row.

Selection note: banner (`## Current Priority`, 2026-08-11) re-verified. M-NIGHTLY
filing done — `ci/logs/action-items.md` still run `20260812-005501`, all four
`## AI-` items already filed. M0131's only unchecked items are S9 (S9.4 measured
and deferred → M0133) and S24 (deferred with ledger + re-arm trigger), so M0131
has nothing selectable. **M0132/M0133 remain "FILED, NOT PROMOTED".**
Fall-through → M0119-0006.

**The finding worth carrying: `xid8` was truncated to 32 bits ON THE HEAP, and
only the twin test could reach it.** Its COPY arm was ACCIDENTALLY correct at
HEAD (`pq_sendint64` = the default's 8 bytes), so the "COPY payload and heap
image agree in width and value, differing only in byte order" pin pointed at the
heap: `encodeValuePG` shared the 4-byte `xid` arm, `physicalPGTypeAlign` returned
the default 4 where `pg_type` 5069 says typlen 8 / typalign `'d'`, and
`internal/wal/pgoutput.go` had the same 4-byte arm (THIRD twin). Same test shape
found the 53rd slice's `float` spelling bug — **write the AgreesWithHeapEncode
pin for every new arm; that is where the adjacent defect lives.**

Candidate 55th slices (cheapest first):
- `interval` binary COPY (16-byte {micros,days,months} — the heap codec already
  builds exactly that image), then `jsonb` (leading version byte), `bpchar`.
- the `reg*`/`cid` family: `regclass`/`regtype`/`regrole`/`regcollation`/
  `regprocedure`/`cid` send as 4-byte identifiers upstream but goopg's heap
  stores them as varlena TEXT — fix the HEAP first, COPY arm in the same slice.
- serial-alias canonicalisation; `AdjustTimeForTypmod` port; zone-less
  `timestamptz`; POSIX `tzparse()`; `box`/`int4range` amcheck key encodings.

Worth carrying:
- `postgres/` here is a real DIRECTORY, not the `../postgres` symlink — psql is
  at `postgres/local_install/bin` relative to the repo root.
- Oracle E2E recipe: `PGPASSWORD=postgres` + `\copy` (server-side `COPY … TO
  '<file>'` is unsupported by goopg), then `cmp` the files. goopg rejects
  `insert into t(oid_col) values (0)` ("has type oid but expression has type
  int8") — use `'0'::oid`. `'0'::regproc` resolves a NAME, so keep regproc out
  of E2E DDL and cover it in unit tests.
- `-c "multi;stmt"` is ONE transaction: a later error rolls the CREATE back.
- Fastest fail-at-HEAD proof: `cp` files aside, `git checkout --` them, run,
  restore. A throwaway `zz_probe_test.go` answers codec questions serverlessly.

Gates: `go test` PASS for executor/wal/catalog/initdb/analyzer, `go build ./...`
+ `go vet` clean, `RALPH_PRECOMMIT_SCOPE=units` PASS, `scripts/tpch-spotcheck.sh`
PASS (Q12=2, Q13=35), oracle E2E on a capped throwaway server (5533) vs PG 18.3
(65432) byte-identical in BOTH COPY directions, pgbench smoke via the commit hook.

In-flight: none.
