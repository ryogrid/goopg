(idle — nothing in flight)

M0119-0006 53rd slice landed — binary `COPY` of `float4`/`float8`. Neither
direction had an arm; because goopg has **no float Datum Kind** (heap decode =
`floatTextDatum(PGFloatOut(…))` ⇒ KindNumeric/KindString), the raw-bytes default
shipped the value's TEXT under FORMAT binary — `0` as one byte, and `+Infinity`
as eight ASCII bytes that a real PG client ACCEPTS as a valid-length float8 and
reads as `5.42e+45` (silent corruption, not an error). Ported
`float4send`/`float8send` = `pq_sendfloat4/8` + recv twins, and extracted the
heap arms' inline coercion into a shared `pgFloatFromDatum(d, bits)` so heap and
COPY cannot drift. Item stays UNCHECKED (standing slice-by-slice cluster);
3 ledger rows resolved, 2 filed; design `0119-0006-copy-binary-float.md` +
README index row.

Selection note: banner (`## Current Priority`, 2026-08-11) re-verified.
M-NIGHTLY filing done — `ci/logs/action-items.md` still run `20260812-005501`,
all four `## AI-` items already filed. **M0132/M0133 remain "FILED, NOT
PROMOTED" — do not pick up without a banner edit.** Fall-through → M0119-0006.

**Two findings worth carrying (both fixed here, both encode/decode drift):**
1. Declared name `float` was in the heap ENCODE spelling list but NOT the
   decode one ⇒ a `float` column wrote 8 fixed bytes and could not read them
   back (`truncated 4-byte varlena`). Same family as the 52nd slice's serial
   finding: goopg stores the DECLARED type name verbatim, so every spelling
   list is a place the twins can silently disagree. **Probe all spellings.**
2. `strconv.ParseFloat("NaN")` sets the low payload bit (`7ff8…0001`); PG's
   `get_float8_nan()` is `7ff8…0000`. Equal as float64, equal under
   `math.IsNaN`, NOT equal as bytes — and the heap image is byte-visible to a
   PG standby. Only the byte-compare caught it (offset 41). Filed: every OTHER
   NaN producer (expr arithmetic, aggregates, `EncodeFloat8`) still bypasses
   the canonicaliser.

Candidate 54th slices (cheapest first):
- **`oid` binary COPY** (4 BE bytes, `oidsend`), then `uuid` (16 raw bytes —
  heap already stores exactly that image), `interval` (16-byte
  {micros,days,months}, heap codec already builds it), `jsonb` (leading version
  byte), `bpchar`. Same encode↔decode + heap-twin + oracle-byte-compare drill.
- the serial-alias canonicalisation (`smallserial`/`serial2/4/8` stored as
  varlena TEXT while `pg_typeof` says `smallint`) — needs its own slice.
- `AdjustTimeForTypmod` port; zone-less `timestamptz` COPY/literal; POSIX
  `tzparse()`; `box`/`int4range` amcheck key encodings.

Worth carrying:
- CLI verb is `init`, NOT `initdb`. `bin/goopg` is NOT rebuilt by
  `scripts/tpch-spotcheck.sh` — `go build -o bin/goopg ./cmd/goopg` first.
- Oracle E2E recipe that worked: `PGPASSWORD=postgres` + **`\copy`** (server-side
  `COPY … TO '<file>'` is unsupported by goopg: "COPY to/from file is not
  supported"). `cmp` the two files directly.
- Binary COPY is BIG-endian, the heap codec little-endian — same value; the
  `…AgreesWithHeapEncode` twin test is the pin for each new arm.
- Fastest fail-at-HEAD proof: `cp` the files aside, `git checkout --` them, run,
  restore. A throwaway `zz_probe_test.go` answers codec questions serverlessly.

Gates: `go test ./internal/executor/` PASS, `RALPH_PRECOMMIT_SCOPE=units` PASS
(mostly cached), `scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35), E2E on a
capped throwaway server (5533) byte-identical to PG 18.3 on 65432 in BOTH COPY
directions incl. NaN/±Inf, pgbench smoke via the commit hook.

In-flight: none.
