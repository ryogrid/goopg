(idle — nothing in flight)

M0119-0006 52nd slice landed — binary `COPY` of `int2`. Both directions fell to
`copy_binary.go`'s raw-bytes default: send emitted 8 BE bytes (the default's
`KindInt` escape) where `int2send` ships 2, so a real PG client rejected the
stream at `CopyReadBinaryAttribute`'s `pq_getmsgend`; recv handed the payload
back via `NewStringDatum`. Ported `int2send`/`int2recv` (`int.c:87`/`:98`) with
two argued departures: length enforced locally (upstream gets it from
`pq_getmsgend`), and 22003 on an out-of-range `Datum.Int` (goopg's is int64).
Item stays UNCHECKED (standing slice-by-slice cluster); 1 ledger row resolved,
2 filed; design `0119-0006-copy-binary-int2.md` + README index row.

Selection note: banner (`## Current Priority`, 2026-08-11) re-verified.
M-NIGHTLY filing done — `ci/logs/action-items.md` still run `20260812-005501`,
all four `## AI-` items already filed (lines 763/767/788/793). M0131's two
unchecked items (S9, S24) are formally closed by ledger rows; M0130 has no
unchecked items. **M0132 and M0133 both say "Priority: FILED, NOT PROMOTED" —
do not pick them up without a banner edit.** Fall-through lands on M0119-0006.

**Biggest finding this loop (own ledger row, own slice):** goopg stores a
column's DECLARED type name, and `codec.go`'s heap arms list `serial`/
`bigserial` but NOT `smallserial`/`serial2`/`serial4`/`serial8` — all four
encode to varlena TEXT `[0x05 '1']` in the heap (vs `[1 0]` for `smallint`)
and ship text through binary COPY, while `pg_typeof` reports `smallint`.
`btree_scalar_keys.go` ALREADY lists `smallserial`/`serial2`, so the index-key
path disagrees with storage today. Prefer canonicalising the declared name ONCE
at CREATE TABLE over adding aliases at ~6 dispatch sites; it is a storage-format
change for existing clusters, so it needs its own slice + gates.

Candidate 53rd slices (cheapest first):
- **`float4`/`float8` binary COPY** — `math.Float{32,64}bits`, then `oid`,
  `uuid`, `interval` (16-byte {micros,days,months}, heap codec already builds
  it), `jsonb` (leading version byte). Same encode↔decode + heap-twin +
  oracle-byte-compare treatment.
- the serial-alias canonicalisation above.
- **`AdjustTimeForTypmod` port** — PG rounds at INPUT, goopg truncates at
  DISPLAY; three consumers. Binary recv additionally needs `copyBinaryToDatum`
  widened (it takes only `catalog.Type`, cannot see `time(N)`).
- zone-less `timestamptz` COPY/literal reads as a UTC wall clock (`tsZoneMode`
  has no "no zone field" arm; MOVES stored instants).
- `'10:00 EST.5'` / POSIX `tzparse()`; `'20200101T040506'`; `box`/`int4range`
  amcheck key encodings; whole-DB unscoped pg_amcheck run.

Worth carrying:
- CLI verb is `init`, NOT `initdb`. `bin/goopg` is NOT rebuilt by
  `scripts/tpch-spotcheck.sh` — `go build -o bin/goopg ./cmd/goopg` first.
- `bench/tpch/env.sh` must be sourced from the REPO ROOT and re-sourced in
  EVERY Bash call hitting 65432/5533. **A `cd postgres` in one Bash call
  persists into later calls** — use absolute paths or re-`cd` the repo root.
- Binary COPY is BIG-endian, the heap codec little-endian — same value; the
  `…AgreesWithHeapEncode` twin test is the pin for each new arm.
- Fastest fail-at-HEAD proof: `cp` the file aside, `git checkout --` it, run,
  restore. A throwaway `zz_probe_test.go` in the package answers codec
  questions without a server.

Gates: `go test ./internal/executor/` PASS, `RALPH_PRECOMMIT_SCOPE=units` PASS
(mostly cached), `scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35), E2E on a
capped throwaway server (5533) byte-identical to PG 18.3 on 65432 in BOTH COPY
directions, pgbench smoke via the commit hook.

In-flight: none.
