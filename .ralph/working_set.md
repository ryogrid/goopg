# Working set — inter-loop baton

Task: **M0143-0007b (banner item 9)** — design doc DONE, slice 1 not started.
Task stays `[ ]`. Owner approval is on record (2026-09-20).

## Why item 9 — the banner walk, recorded so it is not redone

With M0145-0018 now `[!]`, I walked the banner for the first genuinely
selectable task. Under the standing assumption (see escalation 2):

```
item 0  P0            all [x]
item 1  P0-E7 regressions   P0-E7 is [x], no open children
item 2  M0144          0001-0010 [x], 0011 [!]
item 3  M0145          0009-0017 [x], 0018 [!]   (0003-0008 [ ] — contested)
item 4  M0141-S2a-fix2r     [x]
item 5  fix1-sweep / S2b-6-resume / M0139-0007c   all [x]
item 6  M0141-S7      exec-d [ ] but its own "measure first" gate says NO
                      (0 corpus queries reach the operator); S2b-9 and S2b-8
                      BOTH self-declare "not selectable while banner item 6
                      restricts to cost diagnosis only"
item 7  M0140-0007 [ ] — ends in my loop-38 "OWNER DECISION REQUIRED"
item 8  M0142-0005 [!], 0016c [x], 0003i [x]
item 9  M0143-0007b [ ]  <- FIRST ACTIONABLE, owner-APPROVED 2026-09-20
```

## Two escalations still unanswered

1. **M0145-0018 NO-GO** (loop 51) — the firewall relaxation is catastrophic at
   SF1 on the current tree; blocker is the COST MODEL. Owner options are in
   `docs/design/0100-0149/m0145-0018-firewall-relaxation-no-go.md`.
2. **The loop-48 ordering question** — by the strict rule M0145-0003 is the
   first `[ ]` in item 3 (as are 0004, 0005, 0007, 0008). Loops 39-51 worked
   0009 → 0018 assuming those are umbrella items. **Still the banner's call.**

## What the design step found

The task's boundary list is wider than the tree needs. Measured:

- `PGCompareBpcharC` (nbtree) **already** strips trailing blanks via
  `bcTruelen` — upstream's `bpcharcmp` rule — so it is correct under EITHER
  convention. No change.
- `catalog.PadBpchar` pads only a SHORT value, so its four render callers
  (DataRow, COPY text, COPY binary, `octet_length`) become idempotent no-ops.
  No change — and they must NOT be removed, or reads of pre-existing trimmed
  data break.
- What genuinely changes: `coerceTextLikeDatum` (`internal/executor/codec.go`)
  trim → pad, keeping the unbounded-typmod (-1) arm intact; plus the SIZE
  consequences (index max-key-size, TOAST threshold).
- **Safe to land incrementally**: old trimmed and new padded data both read
  correctly, so no migration is needed and `relpages` parity materialises only
  for newly written data.

## Next step

**Slice 1 — the storage flip.** `coerceTextLikeDatum` pads a width-carrying
bpchar instead of trimming. Unit-pin that a `char(10)` datum stores 10 bytes
and an unbounded `bpchar` round-trips verbatim (PG 18.3: `bpchar` 'ab  ' is
octet_length 4 vs `char(6)`'s 6). Sibling audit: re-verify the four
`PadBpchar` callers as no-ops, do not remove them. Gate: own regress run +
SF0.25 sweep.

## Traps carried forward

- **Re-verify preconditions on the CURRENT tree** (loop 51: same A/B, same
  scale, opposite verdict, eleven loops apart).
- `timeout N psql` kills only the client — stop the server, confirm the port.
- Port 5560 is held by a PEER's server; do not touch it. Use 557x.
- A seam change is NOT pipeline-scoped — `tryPGShapedJoinSearch` is shared.

## Gates run

units PASS. No production code changed this loop (design step only), so the
planner corpus gates were not re-run. The commit hook's smoke runs on commit.

## In-flight

none
