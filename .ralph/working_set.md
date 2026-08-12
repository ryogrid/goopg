(idle — nothing in flight)

M0119-0006 55th slice landed — binary `COPY` of `interval`, both directions. A
stored interval is a `KindInterval` Datum matching none of the default's `Kind`
cases, so encode shipped the interval's TEXT under FORMAT binary (21 bytes for
'1 mon 2 days 03:00:00') where `interval_send` is pq_sendint64(time) +
pq_sendint32(day) + pq_sendint32(month) — a fixed 16; decode handed those 16
bytes back as a raw-bytes STRING Datum, putting an interval column right back in
the lexicographic world the heap codec's own interval arm was written to escape.
Fields now come from a shared `pgIntervalFieldsFromDatum` (third repetition of
the pgFloatFromDatum/pgUnsignedIDFromDatum extraction). Item stays UNCHECKED
(standing slice-by-slice cluster); 1 ledger item resolved in place, 2 filed;
design `0119-0006-copy-binary-interval.md` + README index row.

Selection note: banner (`## Current Priority`, 2026-08-11) re-verified. M-NIGHTLY
filing done — `ci/logs/action-items.md` still run `20260812-005501`, all four
`## AI-` items already filed. M0131's only unchecked items are S9 and S24 (both
deferred with ledger rows), M0130 has NO unchecked items, and M0132/M0133 remain
"FILED, NOT PROMOTED". Fall-through → M0119-0006.

**The finding worth carrying: this time the third twin was ALREADY right, and
that is a result, not a null.** The `…AgreesWithHeapEncode` pin that caught the
53rd slice's `float` spelling bug and the 54th's halved `xid8` ran identically
here and came back clean — heap 16 bytes at the right offsets,
`physicalPGTypeAlign` = 8 for typalign 'd', and `internal/wal/pgoutput.go`
already decoding {micros,days,months}. The reason is visible in the ledger:
`interval` got its fixed-width heap layout in a dedicated slice that fixed all
three twins at once, whereas `xid8` had been riding a sibling's arm. **Rule of
thumb for the next slice: expect the adjacent defect when the type SHARES
another type's heap arm; expect clean when it got its own slice.** Keep writing
the pin regardless — clean is now evidence rather than assumption.

Candidate 56th slices (cheapest first):
- `jsonb` binary COPY (leading version byte 1 before the text), then `bpchar`.
  These are the last two named in the 54th slice's ledger row.
- the `reg*`/`cid` family: `regclass`/`regtype`/`regrole`/`regcollation`/
  `regprocedure`/`cid` send as 4-byte identifiers upstream but goopg's heap
  stores them as varlena TEXT — fix the HEAP first, COPY arm in the same slice.
  (Per the rule of thumb above, this family is the one to expect defects in.)
- `AdjustTimeForTypmod`/`AdjustIntervalForTypmod` port — now THREE ledger rows
  deep (49th/51st/55th) and blocked on the same thing: `copyBinaryToDatum` takes
  only `catalog.Type` and cannot see the column typmod. Widen once, fix all.
- serial-alias canonicalisation; zone-less `timestamptz`; POSIX `tzparse()`;
  `box`/`int4range` amcheck key encodings.

Worth carrying:
- `postgres/` here is a real DIRECTORY, not the `../postgres` symlink — psql is
  at `postgres/local_install/bin` relative to the repo root.
- goopg's initdb subcommand is `init` (NOT `initdb`), flags `-D -U -N`.
- Oracle E2E recipe: `PGPASSWORD=postgres` + `\copy` (server-side `COPY … TO
  '<file>'` is unsupported by goopg), then `cmp` the files.
- Fastest fail-at-HEAD proof: `cp` files aside, `git checkout --` them, run,
  restore.

Gates: `go test` PASS for executor/wal/catalog/initdb, `go build ./...` + `go vet`
clean, `RALPH_PRECOMMIT_SCOPE=units` PASS, `scripts/tpch-spotcheck.sh` PASS
(Q12=2, Q13=35), oracle E2E on a capped throwaway server (5533) vs PG 18.3
(65432) byte-identical in BOTH COPY directions plus identical text rendering
after ingest, pgbench smoke via the commit hook.

In-flight: none.
