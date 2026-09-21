# R23: padded `character(N)` on-disk storage (M0143-0007b)

Status: **DESIGN, 2026-09-21.** The task's own first instruction — "If
approved: design doc first" — with the owner's approval on record
(2026-09-20). No production code changed. The boundary inventory below is
measured against the tree, not assumed from the task text, and two of the
boundaries the task names turn out not to need changing.

Task: `.ralph/fix_plan.md` M0143-0007b. Kind: impl. Parent: M0143-0007.

## The defect

PostgreSQL stores a `bpchar` value **blank-padded to its declared width**
(`bpchar_input`, `postgres/src/backend/utils/adt/varchar.c`). goopg stores it
**trimmed**: `coerceTextLikeDatum` (`internal/executor/codec.go`) strips
trailing spaces from any width-carrying bpchar before the heap image is built.

That is a genuine on-disk compatibility defect under the project's
absolute-compatibility rule, and M0143-0007 measured its consequence: it fully
explains the K41 `relpages` gap on `customer` and `item`.

## What the current convention actually consists of

Trimmed storage is not one line; it is a convention with a render half:

- **Storage**: `coerceTextLikeDatum` trims (`codec.go`), guarded by a
  deliberate exception — an UNBOUNDED `bpchar` (typmod -1) is stored verbatim,
  because it has no width to re-pad from. Measured on PG 18.3: `bpchar`
  holding `'ab  '` is `octet_length` 4 where `char(6)` holding the same is 6.
- **Render**: every boundary re-pads through `catalog.PadBpchar` — the
  DataRow path (`internal/postmaster/dispatch.go:4195`), `COPY … TO` text
  (`copy_text.go:419`), `COPY … TO (FORMAT binary)` (`copy_binary.go:435`),
  and `octet_length` (`expr.go:14591`).

## Boundary inventory — measured

The task text lists "heap comparisons, `internal/access/nbtree` key
comparators, `pgoutput` WAL encoding, TOAST thresholds / index-key / WAL record
sizes". Reading each one changes the picture materially:

| boundary | needs a change? | why |
|---|---|---|
| `coerceTextLikeDatum` (`codec.go`) | **YES — this is the change** | trim → pad, keeping the unbounded-typmod exception intact |
| `PGCompareBpcharC` (`nbtree/pgcompare_types.go`) | **NO** | it already strips trailing spaces via `bcTruelen`, upstream's own `bpcharcmp` rule, so it is correct under EITHER convention |
| `catalog.PadBpchar` and its four render callers | **NO** | `PadBpchar` pads only when the value is short, so it is idempotent; with padded storage the calls become no-ops and stay correct |
| index key SIZES (nbtree) | **behaviour change, not a code change** | padded keys are larger, so fanout drops and a key may now exceed the max-key-size limit where it did not |
| TOAST threshold | **behaviour change, not a code change** | a padded value can cross the threshold a trimmed one did not |
| WAL record sizes / `pgoutput` | **verify, likely NO** | no `pgoutput` site calls `PadBpchar`, so its rendering path must be confirmed to derive from the stored datum rather than re-pad independently |

**The two "NO" rows are the load-bearing findings.** They mean the change is
narrower than the task text implies: the comparator is already
padding-insensitive because upstream's is, and the render helpers are already
idempotent. What remains is one storage decision plus the size consequences of
making values physically bigger.

## The compatibility property that makes this safe to land incrementally

Existing on-disk data is trimmed. After the change, a heap page written by an
older binary holds trimmed values while a new one writes padded values, and
**both read correctly**, because:

- comparison ignores trailing blanks (`bcTruelen`, both sides);
- rendering re-pads a short value and leaves a full-width one alone
  (`PadBpchar` is idempotent).

So no rewrite or migration step is required for correctness, and a slice can
land without the cluster being reloaded. The `relpages` parity the defect is
about only materialises for newly written data, which is the honest scope of
the fix and must be said out loud rather than implied.

## Slice plan

Each slice lands with the sibling-path audit the practice card requires, and
each is gated by its own regress run plus the SF0.25 sweep.

1. **Slice 1 — the storage flip.** `coerceTextLikeDatum` pads instead of
   trimming for a width-carrying bpchar; the unbounded-typmod arm is untouched.
   Pin with unit tests that a `char(10)` datum is 10 bytes stored and that an
   unbounded `bpchar` still round-trips verbatim. Sibling audit: the four
   `PadBpchar` render callers must be re-verified as no-ops, not removed —
   removing them would break reads of pre-existing trimmed data.
2. **Slice 2 — size consequences.** Index max-key-size and TOAST-threshold
   behaviour on a padded value. This is where a previously-accepted value can
   start erroring, so it needs its own witness per limit, compared against PG.
3. **Slice 3 — the replication path.** Confirm `pgoutput`'s bpchar rendering
   derives from the stored datum; if it re-pads independently, make it
   idempotent like the others. Gate with the existing pgoutput interop ports.
4. **Slice 4 — the measurement that closes M0143-0007.** Re-measure `relpages`
   on `customer`/`item` against PG after a fresh load, which is the only way
   the original K41 gap can be shown closed.

## Not started

No production code changed this loop. Slice 1 is the next unit of work.
