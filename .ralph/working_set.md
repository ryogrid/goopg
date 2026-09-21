# Working set — inter-loop baton

Task: **M0143-0007b (banner item 9) — SLICE 1 LANDED.** Task stays `[ ]`;
slices 2-4 remain. Owner approval on record (2026-09-20).

## Banner

Unchanged. Item 9's M0143-0007b is still the first actionable task — the
loop-52 banner walk (items 4-8 exhausted or blocked) still holds.

## Two escalations STILL unanswered

1. **M0145-0018 NO-GO** (loop 51) — firewall relaxation catastrophic at SF1;
   blocker is the COST MODEL. Options in that task's design doc.
2. **The loop-48 ordering question** — by the strict rule M0145-0003 is the
   first `[ ]` in item 3. Loops 39-51 worked 0009 → 0018 assuming those are
   umbrella items. **Still the banner's call.**

## What slice 1 did

`coerceTextLikeDatum` (`internal/executor/codec.go`) now blank-pads a
width-carrying bpchar instead of trimming — upstream's strip-then-error-then-pad
order, unbounded-typmod arm untouched, pad by RUNE via `catalog.PadBpchar`.
The K41 `relpages` mechanism is closed for newly written data.

## THE FINDING TO CARRY — the inventory missed a boundary

`length()` rune-counted the stored image directly, so under padded storage
`length('x'::char(4096))` returned **4096** where PG's `bpcharlen` strips
trailing blanks and gives **1**. Fixed via `declaredBpcharTypmod`.

**No unit test covered it, and the SF0.25 sweep, tpch-spotcheck AND the TPC-H
acceptance arm were all GREEN with the bug present.** Only the upstream regress
suite saw it — exactly what Hard-won Rule #5 keeps that gate for.

**Lesson for slices 2-4**: auditing the sites that RE-PAD is not sufficient —
they all route through `PadBpchar` and are idempotent by construction. The
dangerous boundaries are CONSUMERS that read the stored image and assumed it
was trimmed; they do not announce themselves by calling the helper.

Also now commented in the code: `length` STRIPS and `octet_length` PADS — same
type, opposite treatment, because upstream defines them differently. A sibling
pair a later tidy-up must not "unify".

## Next step

**Slice 2 — size consequences.** A padded bpchar can cross the TOAST threshold
or exceed the nbtree max-key-size where the trimmed image did not. Find both
checks, give each a witness value that is accepted trimmed and
rejected/toasted padded, and compare each against **PG 18.3** rather than
against goopg's previous behaviour. Then slice 3 (`pgoutput` — no site calls
`PadBpchar`, so its derivation must be established) and slice 4 (re-measure
`relpages` on `customer`/`item` after a fresh load).

## Traps carried forward

- Run the upstream regress suite after any codec/format change — it is the
  only gate that caught slice 1's regression.
- `timeout N psql` kills only the client — stop the server, confirm the port.
- Port 5560 is held by a PEER's server; do not touch it. Use 557x.

## Gates run

units PASS; upstream regress `char` PASS (172 lines) + `varchar` PASS,
`text`/`strings` fail pre-existing and unrelated (missing HINT; Unicode-escape
and regex parsing), `strings` diff 265 → 263 after the `length()` fix with no
remaining changed line mentioning bpchar; tpch-spotcheck PASS (Q12=2 Q13=33);
TPC-DS SF0.25 PASS=96 MISMATCH=0 CKMISMATCH=0 ERROR=0 TIMEOUT=0, plans 99/99
identical; TPC-H acceptance arm 24 MATCH.

## In-flight

none
