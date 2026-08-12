(idle — nothing in flight)

M0119-0006 56th slice landed — binary `COPY` of `jsonb`, both directions. goopg
carries `json`/`jsonb` as a `KindString` Datum holding the JSON text, so both
halves fell through to the default and were wrong by exactly ONE BYTE at each
end: `jsonb_send` (jsonb.c:124) is `pq_sendint8(1)` + `pq_sendtext(JsonbToCString)`
— encode omitted the version byte, decode failed to strip it. Decode now also
runs `jsonb_from_cstring`'s parse (22P02). `json` deliberately has no arm
(`json_send` IS `textsend`), pinned. Item stays UNCHECKED (standing
slice-by-slice cluster); 1 ledger item resolved in place, 2 filed; design
`0119-0006-copy-binary-jsonb.md` + README index row.

Selection note: banner (`## Current Priority`, 2026-08-11) re-verified. M-NIGHTLY
filing done — `ci/logs/action-items.md` still run `20260812-005501`, all four
`## AI-` items already filed. M0131's only unchecked items are S9 and S24 (both
deferred with ledger rows), M0130 has ZERO unchecked items (verified by line
range 801–2002, not by the `awk /^## M0130/,/^## M0131/` range — those headings
are 3300 lines apart and the naive range counts M0119's items too), and
M0132/M0133 remain "FILED, NOT PROMOTED". Fall-through → M0119-0006.

**The finding worth carrying: a PASSING `…AgreesWithHeapEncode` pin is not
always evidence.** The 55th slice's lesson was "clean is evidence, not
assumption"; this slice qualifies it. The pin passed here because BOTH twins are
wrong together — goopg's heap `jsonb` is varlena TEXT where upstream's is a
`JsonbContainer`/`JEntry` tree. The 55th's rule of thumb (expect the adjacent
defect when the type SHARES another type's heap arm — `jsonb` rides the default
varlena-text arm) was right about WHERE and wrong only about SIZE: the defect is
the storage format itself, too large to absorb, so it is ledgered. **When the pin
passes, ask what upstream's heap image actually IS before calling it agreement.**

Second finding: this defect was SYMMETRIC (encode omits, decode fails to strip),
so goopg↔goopg round-trips perfectly and only the oracle exposes it. The
round-trip guards passed at HEAD; the SHAPE guards (`SendShape`,
`AgreesWithHeapEncode`, `RowFraming`) are what went red. Write shape pins, not
just round-trip pins.

Candidate 57th slice — `bpchar`, the LAST type in this chain, and it should be
bundled: `bpcharsend` IS `textsend` (bytes accidentally right) but `bpchar_recv`
applies `bpchar_input`'s BLANK PADDING to the declared typmod, which needs the
column typmod — the SAME `copyBinaryToDatum` signature widening that the three
`Adjust{Time,Interval}ForTypmod` rows (49th/51st/55th) are blocked on. Widening
`copyBinaryToDatum`/`datumToCopyBinary` to take `catalog.Column` collapses FOUR
ledger rows into one slice. Other candidates: the `reg*`/`cid` family (heap
stores them as varlena TEXT — fix the HEAP first); heap `jsonb` JEntry tree;
`jsonb` input canonicalisation; serial-alias canonicalisation; zone-less
`timestamptz`; POSIX `tzparse()`.

Worth carrying:
- `postgres/` here is a real DIRECTORY, not the `../postgres` symlink — psql is
  at `postgres/local_install/bin` relative to the repo root.
- goopg's initdb subcommand is `init` (NOT `initdb`), flags `-D -U -N`.
- Oracle E2E recipe: `PGPASSWORD=postgres` + `\copy` (server-side `COPY … TO
  '<file>'` is unsupported by goopg), then `cmp` the files. Clean up any `zz_`
  tables you create on 65432 — it is the TPC-H reference cluster.
- Fastest fail-at-HEAD proof: `cp` files aside, `git checkout --` them, run,
  restore.
- A python3 one-liner that mutates a PG-authored binary COPY file (strip a byte,
  fix the int32 length) is the cheapest way to prove "a real PG rejects HEAD's
  stream" without rebuilding goopg at HEAD.

Gates: `go test` PASS for executor/wal/catalog, `go build ./...` + `go vet`
clean, `RALPH_PRECOMMIT_SCOPE=units` PASS, `scripts/tpch-spotcheck.sh` PASS
(Q12=2, Q13=35), oracle E2E on a capped throwaway server (5533) vs PG 18.3
(65432) byte-identical `COPY … TO … (FORMAT binary)` plus identical cross-ingest
in BOTH directions, pgbench smoke via the commit hook.

In-flight: none.
