# M0134-0139 — `macaddr8.sql` macaddr8_in-faithful parsing + `::macaddr`/`::macaddr8` CAST (PARKED)

**Date:** 2026-08-24
**Status:** contained fix landed, PARKED — same closure shape as M0134-0138
(`macaddr.sql`): the remaining diff is dominated by the already-ledgered
btree-opclass-generality gap plus the box/circle/line/lseg/macaddr-shared
LINE-position-echo gap.

## Result

`scripts/pg-regress-runner.sh macaddr8` against PG 18.3: diff went from
**420 lines (0% parity)** to **29 lines**, with only **2 `^+ERROR`** lines
remaining (both the pre-existing btree-opclass gap, not a new mismatch).

## Root cause

Same shape as `macaddr` before M0134-0138: `macaddr8` had **no executor
support at all** — not even raw-varlena pass-through validation. Worse,
sizing this file live surfaced a *second*, previously-unknown gap that
`macaddr.sql` never exercised: **`::macaddr` and `::macaddr8` CAST were both
unvalidated pass-throughs** in `evalCast` — the switch had no `case
"macaddr"`/`"macaddr8"` at all, so any string fell through to the function's
final `return d, nil`. `'garbage'::macaddr8` silently "succeeded" with the
garbage stored verbatim, and every `-- invalid` row in the file's opening
block returned a bogus success row instead of the expected `22P02` error.

## What landed

1. **`parseMacaddr8Literal`** (`internal/executor/expr.go`) — a faithful port
   of `macaddr8_in` (`postgres/src/backend/utils/adt/mac8.c:96-232`).
   Structurally different from `macaddr_in`'s 7-format `sscanf` cascade:
   `macaddr8_in` is a **single greedy scanner** that reads exactly 2 hex
   digits at a time as one byte (no field-width variants), optionally
   followed by one of `:`/`-`/`.` as a separator — once a separator
   character is seen it must be used consistently for the rest of the
   string, but the separator's *position* is not tied to any grouping (e.g.
   `'0800:2b01:0203'` is valid input: the parser doesn't require the colon
   between every byte pair, only that whichever separator appears stays the
   same throughout). Exactly 6 or 8 bytes are accepted; a 6-byte (EUI-48)
   address auto-widens to EUI-64 by inserting `0xFF`/`0xFE` as the 4th/5th
   octets and shifting the trailing 3 bytes down — the identical transform
   `macaddrtomacaddr8` performs explicitly, so a bare 6-byte literal and a
   `::macaddr8`-cast macaddr value produce the same result through one code
   path.

2. **`macaddr8CanonicalText`** — `macaddr8_out`'s fixed
   `"%02x:%02x:%02x:%02x:%02x:%02x:%02x:%02x"` format.

3. **`macaddr8TruncOctets`** (mac8.c:476-497) — keeps only the 3-byte OUI
   prefix (`a,b,c`), zeroing all 5 trailing octets. This differs from
   `macaddrTruncOctets`, which keeps 3 of *6* octets; `macaddr8_trunc` keeps
   3 of *8*.

4. **`macaddr8Set7BitOctets`** (mac8.c:499-521) — ORs the first octet with
   `0x02` (modified-EUI-64 form for IPv6 interface identifiers), used by the
   new `macaddr8_set7bit()` function dispatch.

5. **`macaddr8ToMacaddrOctets`** / **`macaddrToMacaddr8Octets`** — the two
   conversion functions (`macaddr8tomacaddr`/`macaddrtomacaddr8`,
   mac8.c:523-566). The macaddr8→macaddr direction requires the 4th/5th
   octets be exactly `0xFF`/`0xFE` or raises `22003` with PG's exact HINT
   text; the macaddr→macaddr8 direction is the unconditional widen.

6. **`::macaddr`/`::macaddr8` CAST** (`evalCast`, `expr.go`) — new cases,
   previously entirely absent:
   - `case "macaddr"`: try `parseMacaddrLiteral` first (a genuine 6-octet
     macaddr source parses directly, no round trip); on failure, try
     `parseMacaddr8Literal` and run the result through
     `macaddr8ToMacaddrOctets`'s FF/FE check — this is how `b::macaddr` on a
     macaddr8 column value converts.
   - `case "macaddr8"`: try `parseMacaddrLiteral` first and widen via
     `macaddrToMacaddr8Octets` (mirrors `macaddrtomacaddr8` exactly); on
     failure, try `parseMacaddr8Literal` directly (covers both a genuine
     8-byte source and a bare 6-byte literal, since that function already
     performs the identical widening internally).
   - Both previously fell to the function's trailing `return d, nil`
     pass-through.

7. **Wiring**, mirroring the macaddr chokepoint pattern: column-assignment
   coercion (`codec.go`), `pg_input_is_valid`/`pg_input_error_info`
   (`expr.go` + `operators_pg_input_error_info.go`), and the `~`/`&`/`|`
   bitwise operators / `trunc()` function (tried **after** the 6-octet
   `macaddr` form in each shared dispatch site, so an 8-field colon literal
   — which never matches `parseMacaddrLiteral`'s trailing-garbage check —
   falls through cleanly to the macaddr8 arm).

## Not fixed this loop

Same two already-ledgered components as M0134-0138:

1. **psql LINE-position echo** — `coerceTextLikeDatum` never attaches
   `ExecError.Pos`. Resume point unchanged from the M0134-0094/.../-0138
   rows.

2. **`CREATE INDEX ... USING btree/hash (b)` on a macaddr8 column** — raises
   the pre-existing `0A000 btree v0 only supports int4 / numeric keys, got
   "macaddr8"` opclass-generality gap (M0134-0060/-0067/-0138). Needs the
   identical `macaddr8_cmp` (hi/lo `unsigned long` compare, mac8.c:307-321)
   key-encoding treatment `macaddr` does — not attempted this loop for the
   same reason as M0134-0138 (large, cross-case architectural item).

## Verification

- `go build ./...` — PASS.
- `go test -run TestMacaddr8 ./internal/executor/` — PASS (3 subtests:
  parse validation, set7bit/trunc, macaddr8-to-macaddr FF/FE range check).
- `scripts/pg-regress-runner.sh --verbose macaddr8` — 420→29 diff lines, 2
  `^+ERROR` (both the pre-existing btree-opclass gap).
- `scripts/pg-regress-runner.sh macaddr box circle line lseg point inet` —
  all held steady at their known baselines (33/722/51/55/27/531/1298 diff
  lines respectively): no regression from the new `evalCast` macaddr/
  macaddr8 cases or the macaddr8 detection added to the shared `~`/`&`/`|`/
  `trunc()` code paths.
