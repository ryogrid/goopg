# R23: padded `character(N)` on-disk storage (M0143-0007b)

Status: **SLICES 1-3 LANDED 2026-09-22.** Storage is padded, the padding is
applied before the TOAST decision, and the functions that upstream defines on
the TRIMMED value now trim explicitly instead of relying on the old storage
shape. Slice 4 (re-measure `relpages`) remains. The boundary inventory below was
measured before coding and two of the boundaries the task names did not need
changing — but it also MISSED two, each caught by a different gate; see "What
the inventory missed" and "Slice 2".

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

## Slice 1, landed

`coerceTextLikeDatum` (`internal/executor/codec.go`) now blank-pads a
width-carrying bpchar instead of trimming it, keeping upstream's two steps in
upstream's order — excess trailing spaces stripped silently, 22001 only if the
value is still too long — and leaving the unbounded-typmod arm untouched. The
pad goes through `catalog.PadBpchar`, so it counts runes exactly as
`bpchar_input`'s `pg_mbstrlen_with_len` does.

Four existing tests pinned the old convention and were updated with their
rationale rewritten rather than their expectations merely flipped;
`TestCopyBinaryBpcharRoundTripsToTrimmedStorage` was renamed to
`...ToPaddedStorage`, since the invariant it protects — one column, one stored
width, whatever loaded it — is unchanged and only the width it agrees on moved.
`TestBpcharStoredPaddedAndRenderBoundariesStayInert` is new and pins slice 1's
contract together with its sibling audit, including that a PRE-FLIP trimmed
image still renders at full width.

## What the inventory missed, and how it surfaced

The inventory above lists the render boundaries that PUT padding back — all of
which go through `catalog.PadBpchar` and are therefore idempotent. It missed a
CONSUMER that depended on the trimmed convention without going through that
helper:

**`length()`** (`internal/executor/expr.go`) rune-counted the stored value
directly. Under trimmed storage that accidentally produced PostgreSQL's answer,
because PG's `length()` on bpchar is `bpcharlen`, which counts characters AFTER
stripping trailing blanks (`bcTruelen`, `varchar.c`). Under padded storage it
returned the declared width: `length('x'::char(4096))` became 4096 where PG
gives 1.

It was caught by the upstream `strings` regress case, which is exactly what
Hard-won Rule #5 ("after codec/format changes, re-run the full regress-port
suite") exists for — no unit test in the tree covered it, and the SF0.25 sweep,
the spotcheck and the acceptance arm were all green with the bug present.

The fix applies `bpcharlen`'s rule explicitly, gated on the argument's declared
bpchar type via the same `declaredBpcharTypmod` helper `octet_length` already
used. Note the asymmetry the code now carries a comment about: `length` STRIPS
and `octet_length` PADS, same type, opposite treatment, because upstream
defines the two functions differently. That is a sibling pair that must not be
"unified" by a later tidy-up.

**Generalised lesson for slices 2-4**: auditing the sites that re-pad is not
sufficient. Any consumer that reads the stored image and assumed it was trimmed
is equally a boundary, and those do not announce themselves by calling
`PadBpchar`.

## Gates run for slice 1

units PASS; upstream regress `char` PASS (172 lines) and `varchar` PASS — the
two cases that directly exercise this type; `text` and `strings` fail on
pre-existing, unrelated grounds (a missing HINT line; Unicode-escape and regex
parsing), and the `strings` diff shrank from 265 to 263 lines when the
`length()` hunk was fixed, with no remaining changed line mentioning bpchar.
tpch-spotcheck PASS (Q12=2 Q13=33); TPC-DS SF0.25 PASS=96 MISMATCH=0
CKMISMATCH=0 ERROR=0 TIMEOUT=0 with plans 99/99 identical; TPC-H acceptance arm
24 MATCH.

## Slice 2 — the size consequences, measured against a private PG 18.3

The design guessed slice 2 would be about values that *start erroring* at the
index max-key-size or TOAST threshold. A private PostgreSQL 18.3 instance
(`initdb` in `/tmp`, port 5581 — no reference cluster touched) says otherwise,
and the real finding is bigger.

### What PG actually does

```
CREATE TABLE t(c char(3000)); INSERT INTO t VALUES('x');
  octet_length 3000 | length 1 | pg_column_size 45
CREATE INDEX on char(3000) and char(8000), INSERT 'x' — both SUCCEED
```

`pg_column_size` 45 for a 3000-byte value: upstream **compresses the padding
away**. Nothing errors, because pglz reduces a run of blanks to almost nothing,
so neither the TOAST threshold nor `BTMaxItemSize` is reached in practice. The
"previously-accepted value starts erroring" case the design anticipated does
not arise for blank padding.

### What goopg did after slice 1

```
200 rows of char(3000) holding 'x':
  PG 18.3        heap    16,384 bytes
  goopg slice 1  heap   819,200 bytes     (50x)
```

Root cause, confirmed by reading the call order rather than inferred: goopg
pads in `coerceTextLikeDatum`, which runs inside `encodeValuePGCtx` — i.e.
**after** `ToastLargeColumnsIfNeeded` has already decided. The toast check saw
the 1-character datum, declined, and the encoder then wrote 3000 raw bytes
inline. Upstream pads at INPUT (`bpchar_input`), so its decision sees the
padded value.

This was a regression slice 1 introduced: before it, the stored value was 2
bytes (smaller than PG); after it, 819 KB (much larger). The direction flipped,
and the magnitude got worse.

### The fix, and it restores upstream's ORDER

`ToastLargeColumnsIfNeeded` now pads a width-carrying bpchar before the
threshold check, writing the padded datum back into the row so the later encode
finds it already full width (`PadBpchar` is idempotent). Pad, then decide —
upstream's sequence.

```
goopg after slice 2  heap 16,384 bytes — byte-identical to PG 18.3
values unchanged: count 200, octet_length 3000, length 1
```

### Why no corpus gate saw it

The SF0.25 sweep, the tpch-spotcheck and the TPC-H acceptance arm were **all
green at 819 KB**, because every VALUE was correct throughout — only the bytes
on disk were wrong, which is precisely what R23 is about. `TestToastPadsBpcharBeforeDeciding` is the witness, and it is a unit test for that reason.

## Slice 3 — the pgoutput path, and two more consumers

### The design's claim about pgoutput was wrong

This document said "no pgoutput site calls `PadBpchar`". It does:
`pgoDecodePhysicalValue` (`internal/access/transam/xlog/pgoutput.go`) applies
it to every varlena payload, and `TestPgoDecodeBpcharCarriesDeclaredWidth`
already pinned it. The earlier claim came from grepping a package path that
does not exist (`internal/wal/`) rather than from finding the emitter. The
boundary is covered and, like the other three, is now a no-op on newly written
rows while remaining necessary for pre-flip trimmed ones.

### What slice 2 did change at that boundary

`pgoDecodePhysicalValue` **fails loudly** on an external TOAST pointer
("logical replication of toasted values is not supported"). Slice 2 moved wide
bpchar values into the toasted population, so a column like `char(3000)` now
hits that pre-existing v0 limitation where it previously did not. The
limitation is not new and is explicitly acknowledged in the code; the
population reaching it is. Ledgered.

### Two more consumers that assumed trimmed storage

Measured on PG 18.3 against a STORED `char(10)` holding `'ab'`:

```
                  PG 18.3   goopg before   after
length(c)               2              2       2
octet_length(c)        10             10      10
bit_length(c)          16             80      16
length(c::text)         2             10       2
```

The root of both misses is one upstream fact: **`char(n) -> text` is `rtrim1`**
(`pg_proc.dat` oid 401, "convert char(n) to text"), so the cast STRIPS the
padding. `bit_length` has no bpchar overload and resolves through exactly that
cast, which is why it reports the trimmed length while `octet_length` reports
the padded one.

goopg satisfied both by accident while storage was trimmed. The fixes apply
upstream's rule explicitly: the cast rtrims in `evalCastTyped`, and
`bit_length` applies the same rule via `declaredBpcharTypmod`, as `length`
already does after slice 1.

**Why the existing test did not catch it**: the literal forms
(`'ab'::char(10)`) never reach `coerceTextLikeDatum`, so their datum stayed
trimmed and the old agreement held. Only a STORED column diverged, and
`TestBpcharStoredColumnLengthFamilyMatchesPG` is the witness that distinguishes
them.

The `strings` regress diff shrank 263 -> 248 lines with this fix.

### The comments, corrected

Every comment asserting "goopg stores bpchar trimmed" now said the opposite of
the truth. `internal/catalog/bpchar.go`, `pgoutput.go`, `pgoutput_bpchar_test.go`
and `expr.go`'s `octet_length` note were rewritten, each also recording WHY the
`PadBpchar` call must stay: pre-flip rows on disk are trimmed.

## Next

Slice 4 — reload and re-measure `relpages` on `customer`/`item` against PG.
That is the only way K41's original gap is shown closed rather than its
mechanism.
