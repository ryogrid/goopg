# M0134-0130 — inet/cidr column-assignment normalization

**Status:** accepted (partial — case PARKED, see Deferred).
**Task:** `.ralph/fix_plan.md` M0134-0130 (`inet.sql`).
**Ledger:** `.ralph/deferral_ledger.md`, 2026-08-24, M0134-0130.

## Problem

`scripts/pg-regress-runner.sh inet` sized at 0% parity, 1397 diff lines
against the PG 18.3 oracle. Root cause: goopg's `inet`/`cidr` types had **zero
input canonicalization and zero output formatting**. A value assigned to an
`inet`/`cidr` column was stored and re-emitted as raw text, verbatim:

- `INSERT INTO t (c) VALUES ('10')` (cidr column) stored the literal string
  `"10"` and `SELECT c FROM t` printed `"10"` back — never expanded to PG's
  canonical `10.0.0.0/8` (classful-default-mask expansion).
- `'192.168.1.2/30'::cidr` (host bits set to the right of the /30 mask) was
  silently accepted instead of raising `22P02 invalid cidr value` with
  `DETAIL: Value has bits set to right of mask.`
- No non-abbreviated canonical text form existed at all — PG's `cidr_out`
  always shows the dotted-quad address plus `/bits`, even at `/32`
  (`10.0.0.0/32`, not `10.0.0.0`); `inet_out` omits `/bits` only when the mask
  is full-width.

This wasn't a display bug in isolation — it meant `cidr`/`inet` values held no
real semantic content beyond "opaque string", which is why the file's ~90%
diff was almost entirely display-format mismatches plus every function/
operator built on top of a real network-address representation.

## Fix

Added three new pieces to `internal/executor/operators_ddl.go`, next to the
existing btree-key parser (`parseInetKeyText`, `cidrDefaultV4Mask`,
`maskInetAddr` — M0134-0002 C5):

- **`normalizeInetCidrText(s string, isCidr bool) (string, *ExecError)`** —
  the single chokepoint. Parses via the existing `parseInetKeyText`, applies
  the cidr host-bit-violation check (`22P02` + DETAIL, mirroring
  `network_in`, `postgres/src/backend/utils/adt/network.c:74-113`), then
  renders via `formatInetAddr`.
- **`formatInetAddr`/`formatInetV4`/`formatInetV6`** — a faithful Go port of
  `pg_inet_net_ntop` (`postgres/src/port/inet_net_ntop.c:113-330`) plus
  `network_out`'s "for CIDR, add /n if not present" step
  (`network.c:150-157`, why cidr always shows a mask even at `/32`).

### Why `net.IP.String()` couldn't be reused for IPv6

PG's `inet_net_ntop_ipv6` embeds the last 32 bits as dotted-decimal whenever
the leading zero-word run is *exactly* 6 words (or 7 with a nonzero last
word, or 5 with `word[5] == 0xffff`) — unconditionally, not just for the
well-known `::ffff:a.b.c.d` v4-mapped form. Go's `net.IP.String()` only
special-cases that one well-known form. Confirmed live:

| input | Go `net.IP.String()` | PG / goopg (this fix) |
|---|---|---|
| `::ffff:1.2.3.4` | `1.2.3.4` (collapses to bare v4) | `::ffff:1.2.3.4` |
| `::4.3.2.1` | `::403:201` (pure hex groups) | `::4.3.2.1` |

Both are wrong for PG's `inet`/`cidr` text output, so `formatInetV6` reimplements
the word-scan + best-run + embedded-v4-tail algorithm directly.

### Wiring: column assignment only, not explicit cast

Wired into `coerceTextLikeDatum` (`internal/executor/codec.go`) — the same
chokepoint `box`/`circle` canonicalize-on-assignment through (M0134-0094/
-0098). **Deliberately not wired into `evalCast`**: `box`/`circle` don't
validate the explicit `'...'::box` cast boundary either (no case in
`evalCast`'s switch), so leaving `'...'::inet` unvalidated there is
consistent with the established precedent, not a new gap — ledgered
alongside for a future sweep if that precedent gets revisited.

## Verification

- New test `TestNormalizeInetCidrText`
  (`internal/executor/inet_cidr_normalize_test.go`, 14 subtests): classful
  default-mask expansion (Class A/B/C), full-host `/32` (mask always shown
  for cidr), explicit `/n`, `inet`'s mask-omitted-at-full-width behavior,
  cidr host-bit violation (`22P02` + DETAIL), malformed address (`22P02`),
  and four IPv6 cases (v4-mapped, v4-compatible-embedded, plain compressed,
  with explicit mask).
- Live server run of the real `postgres/src/test/regress/sql/inet.sql`
  against a throwaway cgroup-capped goopg confirmed the `cidr`/`inet` column
  SELECT now byte-matches PG's canonical form (`192.168.1.0/24`,
  `10.0.0.0/32`, `::ffff:1.2.3.4/128`, `::4.3.2.1/24`) and the two
  input-validation error cases now raise the correct SQLSTATE/message.
- `scripts/pg-regress-runner.sh inet`: 1397 → 1298 diff lines.
- `go build ./...`; `go test ./internal/executor/...` PASS.
- `scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35).
- `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS.

## Deferred (see ledger row for full detail)

1. **11 `pg_proc`-seeded-but-undispatched scalar functions** dominate the
   remaining diff: `host`/`abbrev`/`broadcast`/`network`/`masklen`/
   `netmask`/`hostmask`/`inet_merge`/`inet_same_family` plus the
   `cidr(text)`/`inet(text)` constructor functions. Same shape as the
   `hash_func.sql` gap (M0134-0128) — OID/RetType/ArgTypes already seeded,
   zero `evalFuncCall` dispatch. Each reduces to the primitives this loop
   built (`formatInetAddr`/`parseInetKeyText`/`maskInetAddr`), so this is
   comparatively low-effort follow-on wiring.
2. **`<<`/`<<=`/`>>`/`>>=`/`&&`/`~`/`&` inet/cidr operator family** —
   entirely unparsed; the lexer has no tokens for these in inet/cidr
   position, not just missing backend functions.
3. **No `cidr`↔`inet` implicit comparison coercion** — `WHERE c = i` between
   a cidr and an inet column raises `operator = has incompatible operand
   types`; PG coerces both sides to `inet`.

## Related

- [`docs/design/m0134-0128-hash-func-scalar-family.md`](m0134-0128-hash-func-scalar-family.md) — same "seeded-but-undispatched scalar function family" pattern.
- [`docs/design/m0134-0094...`](../../README.md) — box/circle canonicalize-on-assignment precedent this fix follows.
