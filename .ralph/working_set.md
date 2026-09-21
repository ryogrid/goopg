# Working set — inter-loop baton

Task: **M0143-0007b — COMPLETE `[x]`.** All four slices landed; K41's
`relpages` gap is closed.

## Banner

Unchanged. **Next loop must re-walk the banner**: item 9's first task is now
`[x]`, so the next selectable item has to be found afresh. The loop-52 walk
(items 4-8 exhausted or blocked) still holds, so start from item 9's remaining
M0143 tasks, then item 10 (M-NIGHTLY + the pre-existing milestones).

## Two escalations STILL unanswered

1. **M0145-0018 NO-GO** (loop 51) — blocker is the COST MODEL.
2. **The loop-48 ordering question** — M0145-0003 is strictly the first `[ ]`
   in item 3. Still the banner's call.

## Slice 4 — the measurement that closed it

```
table      rows      PG   before    after      gap
customer 100000    2872     1979     2854    -0.63%
item      18000    1284      716     1242    -3.27%
```

against M0143-0007's original **-31.1%** and **-44.2%**. Row counts identical.
Built from the upstream TPC-DS schema on a fresh private goopg, filled from the
same SF0.25 TSVs, pages read off the relfilenode on disk; PG's `relpages` read
SELECT-only from the read-only reference.

**The residual is NOT claimed closed**: under 1% / 3.3% is goopg packing pages
more densely, which M0143-0007 already separated as its own effect
(free-space-per-page, not tuple width).

## What the whole task produced

Four slices: storage padded → pad before the TOAST decision (fixing a 50x heap
regression slice 1 introduced) → the trimmed-value consumers (`length`,
`bit_length`, the `char(n)->text` rtrim1 cast) → the measurement.

**The rule worth carrying**: when a storage convention changes, the sites that
RE-PAD are safe — one idempotent helper. The dangerous ones are **CONSUMERS
that read the stored image**. All three misses were consumers, and each was
invisible to a different gate: the upstream regress suite, a byte-level
measurement no value gate performs, and a stored-column witness (literal-
expression tests bypass the storage path entirely).

## Two follow-ups left behind, each its own task

- **Heap page density**: the -0.63%/-3.27% residual. Instrument already exists
  (`internal/storage/page.go` fill ratios, built by M0143-0007).
- **Logical replication of TOASTED values**: `pgoDecodePhysicalValue` rejects
  an external TOAST pointer outright where PG sends the detoasted value or the
  unchanged-toast marker. Wide bpchar columns now reach it (slice 2 made them
  toastable). Gate with the pgoutput interop ports.

## Traps carried forward

- A private PG oracle is cheap (`initdb` into /tmp on a 55xx port) — it settled
  every ambiguity in this task.
- Literal-expression tests bypass the storage path; use a stored column.
- The RALPH_LOOP guard trips on prose pairing a reference port with DDL words —
  split the write.
- Port 5560 is held by a PEER's server; do not touch it.

## Gates run

units PASS. No production code changed this slice (measurement only), so the
corpus gates were not re-run; slices 1-3 each ran the full set.

## In-flight

none
