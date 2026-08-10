(idle — nothing in flight)

Last loop (#95): M0119-0006 30th slice — **the BC era is ordinary date/time
input again, and the probe found the nanosecond carrier underneath it**.
Committed + pushed. Design `docs/design/0125-0007-pg-faithful-date-field-decode.md`
§8 (+ README row edit), 1 ledger row.

M-NIGHTLY duty this loop: `ci/logs/action-items.md` is still nightly run
`20260811-014635` (12 items), all already filed under M-NIGHTLY (loop #87).
Nothing new to file; they stay PARKED per the banner.

What landed: `internal/pgdatetime/era.go` (`SplitEra`/`ApplyEra`/`EraYear`) —
PG's trailing `ADBC` token, case-insensitive, whitespace optional, digit-before
-the-token so `'BC BC'`/`'BC'`/`'B.C.'` stay refused; era → astronomical year
(1 BC = year 0); PG's no-year-zero rule as 22008. The whole ENTRY POINT is now
shared (`parsePGTimestampText`/`parsePGDateText` in copy_text.go behind the
literal, cast, COPY and `pg_input_is_valid` paths) — sharing only the layout
table was not enough, this was the 4th consecutive drift. Output: `eraDisplay`
in `internal/config/datestyle.go`, all four DateStyles, `Postgres`-style weekday
still from the real instant.

What it FOUND (bigger than the era): `KindTime` Datum stores int64 NANOSECONDS
since 1970 (`datum.go`), i.e. 1677..2262, where PG stores MICROSECONDS since
2000 (4713 BC..294276 AD). Outside that window `UnixNano` overflows and Go keeps
the wrapped value, so goopg silently answered `'1000-01-01'::date` → 2169-02-08,
`'2300-01-01'` → 1715-06-13, `'0000-01-01'` → 1753-08-29. This slice makes it
LOUD (22008 naming the range); a BC date can be READ but not STORED. Honest
cost: acceptance regression on valid far dates, and two in-tree tests that
compared ±infinity to `'9999-12-31'`/`'0001-01-01'` moved to the representable
extremes.

Next loop: per banner (M0130 fully checked → M0119-0006 is the actionable head).
Strongest candidate is now the CARRIER: move `Datum.Int` for `KindTime` from
nanoseconds to MICROSECONDS (PG's unit; ±292k years) — `NewTimeDatum`/
`NewDateDatum`/`NewTimeTZDatum`/`TimeValue`, then audit every `.Int` consumer
(storage codec, wire binary in server/dispatch.go, sort/hash/spill, interval
arithmetic) and delete `checkTimeCarrierRange`. Needs the FULL gate battery — a
single missed consumer scales every timestamp by 1000. Cheaper alternatives:
`'10:00.5'`/`'10::00'` field roles, `'2020-01-01Z'`; numeric index-key display
scale (ledger resume point SUSPECT); array slices `a[1:2]` (lexer); TOASTed /
multi-dim / NULL-element arrays in logical decoding.

Gates: build + vet clean; units PASS; `TestPort_RegressSuite` PASS (632 s, warm
cache — needs `-timeout 45m`); `scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35);
pgbench smoke via the commit hook. Wants captured from a throwaway PG 18.3
cluster (socket /tmp, port 5599, datadir /tmp/pgoracle-loop94 — stopped).

In-flight: none
