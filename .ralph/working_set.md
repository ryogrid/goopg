# Working set — inter-loop baton

Task: **M0143-0007b (banner item 9) — SLICES 1 AND 2 LANDED.** Task stays `[ ]`;
slices 3-4 remain. Owner approval on record (2026-09-20).

## Banner

Unchanged. Item 9's M0143-0007b is still the first actionable task — the
loop-52 banner walk (items 4-8 exhausted or blocked) still holds.

## Two escalations STILL unanswered

1. **M0145-0018 NO-GO** (loop 51) — firewall relaxation catastrophic at SF1;
   blocker is the COST MODEL.
2. **The loop-48 ordering question** — by the strict rule M0145-0003 is the
   first `[ ]` in item 3. **Still the banner's call.**

## Slice 2, and the design's guess about it was WRONG

A private PG 18.3 (`initdb` in /tmp, port 5581 — no reference cluster touched)
settled what the design had guessed at:

```
PG: char(3000) holding 'x' -> octet_length 3000, length 1, pg_column_size 45
    indexes on char(3000) AND char(8000) both accept the insert
=> PG COMPRESSES the padding; nothing errors at TOAST/BTMaxItemSize.
```

So the anticipated "previously-accepted value starts erroring" case does not
arise. What goopg actually had was a **50x REGRESSION from slice 1**:

```
200 rows of char(3000):   PG 16,384 bytes   goopg-slice-1 819,200 bytes
root cause: coerceTextLikeDatum pads inside encodeValuePGCtx — AFTER
ToastLargeColumnsIfNeeded decides. Upstream pads at INPUT (bpchar_input).
fix: pad before the threshold check -> goopg 16,384, byte-identical to PG.
```

## THE FINDING TO CARRY

**No corpus gate can see this class.** The SF0.25 sweep, tpch-spotcheck and
TPC-H acceptance arm were ALL green at 819 KB, because every VALUE was correct
and only the bytes on disk were wrong — which is exactly what R23 is about. A
unit test is the right witness for a storage-shape defect.

This is the SECOND boundary slice 1's inventory missed (the first, `length()`,
was caught by the upstream regress suite). Both were consumers or decision
points that READ the stored image, not sites that re-pad it.

## Next step

**Slice 3 — the `pgoutput` path.** No pgoutput site calls `PadBpchar`, so
whether its bpchar rendering derives from the stored datum (now padded, hence
correct by construction) or re-renders from something that is not, is
unestablished. A subscriber could receive a differently-padded value than PG
would send. Gate with the existing pgoutput interop ports in
`internal/testport/`. Then slice 4: reload and re-measure `relpages` on
`customer`/`item` against PG — the only way K41's gap is shown closed.

## Traps carried forward

- Value gates are structurally blind to storage-shape defects — measure bytes.
- Run the upstream regress suite after any codec/format change.
- A private PG oracle is cheap: `initdb` into /tmp on a 55xx port. Use it
  instead of reasoning about upstream behaviour.
- Port 5560 is held by a PEER's server; do not touch it.

## Gates run

units PASS; upstream regress `char` PASS (172 lines) + `varchar` PASS,
`strings` unchanged at 263 lines (pre-existing Unicode/regex failures);
tpch-spotcheck PASS (Q12=2 Q13=33); TPC-DS SF0.25 PASS=96 MISMATCH=0
CKMISMATCH=0 ERROR=0 TIMEOUT=0, plans 99/99 identical; TPC-H acceptance arm
24 MATCH.

## In-flight

none
