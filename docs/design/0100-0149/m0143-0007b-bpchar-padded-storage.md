# R23: padded `character(N)` on-disk storage (M0143-0007b)

Status: **COMPLETE 2026-09-22 - all four slices landed.** Storage is padded,
the padding is applied before the TOAST decision, the functions upstream
defines on the TRIMMED value trim explicitly, and slice 4 measured the result:
**K41's `relpages` gap is closed**, from -31%/-44% to -0.6%/-3.3%. The boundary inventory below was
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

## Slice 4 - K41's gap, re-measured and closed

The mechanism being right is not the same as the number being right, so slice 4
reloads and measures. `customer` and `item` were built from the upstream TPC-DS
schema on a private goopg (fresh datadir, current binary) and filled from the
same SF0.25 TSVs the benchmark cluster used; page counts were read off the
relfilenode on disk. PG's side is `pg_class.relpages`, read SELECT-only from
the read-only TPC-DS reference cluster.

```
table      rows      PG   before    after      gap
customer 100000    2872     1979     2854    -0.63%
item      18000    1284      716     1242    -3.27%
```

Row counts match exactly on both sides (100,000 and 18,000). The `before`
column is M0143-0007's original K41 measurement, at -31.1% and -44.2%.

**The gap is closed.** What remains - under 1% on `customer`, 3.3% on `item` -
is goopg packing pages slightly more densely, which M0143-0007 had already
separated out and measured as its own smaller effect ("goopg packs pages MORE",
free-space-per-page rather than tuple width). That residual is not this task's
and is not claimed as closed by it.

Why `customer` and `item` are the right witnesses: between them they carry
eight `char(N)` columns totalling ~150 characters per row (`c_customer_id` 16,
`c_salutation` 10, `c_first_name` 20, `c_last_name` 30, `c_login` 13,
`c_email_address` 50, and so on), so the padding is a large fraction of the
tuple - which is exactly why K41 surfaced on them and not on the fact tables.

## Summary of the four slices

1. **Storage** - `coerceTextLikeDatum` pads instead of trimming.
2. **TOAST order** - pad before the toast decision, as `bpchar_input` does;
   fixed a 50x heap regression slice 1 had introduced on wide columns.
3. **Consumers** - `length`, `bit_length` and the `char(n) -> text` cast
   (`rtrim1`) apply upstream's trimming rules explicitly; the pgoutput
   boundary was verified as already correct.
4. **Measurement** - K41's gap closed, confirmed against the live reference.

The lesson the task produced, confirmed three times: when a storage convention
changes, the sites that RE-PAD are safe because they route through one
idempotent helper, and the dangerous ones are CONSUMERS that read the stored
image. Each of the three misses was invisible to a different gate - the
upstream regress suite, a byte-level measurement, and a stored-column witness.

## Follow-on (2026-09-22): the PRODUCER side, found by the upstream regress suite

M0143-0007b closed with all four slices landed and the `relpages` number
measured. Two nights later the nightly's `TestPort_RegressSuite` failed on
four subtests, and three of them (`select_having`, `select_implicit`,
`union`) traced back to this task — to the half the four slices never
examined.

Slices 1–3 audited **consumers**: sites that read the stored image and had
assumed it was trimmed (`length`, the TOAST decision, `bit_length`, the
`char(n)->text` cast). The gap found now is on the **producer** side: after
the storage flip, the CAST *to* `char(n)` was the only bpchar producer still
emitting a trimmed image. So goopg had two representations of the same
logical value, and everything downstream that compares them broke.

| path | upstream rule | oracle |
|---|---|---|
| cast TO `char(n)` | pads | `varchar->bpchar` is `castfunc => '0'` in `pg_cast.dat`, so the typmod coercion `bpchar()` (varchar.c) pads |
| `bpchar -> varchar` | strips | `pg_cast.dat`: `castfunc => 'text(bpchar)'` — i.e. `rtrim1`, the same function as `bpchar -> text` |
| `lower`/`upper`/`initcap` on a bpchar | strips | no `lower(bpchar)` exists; the call resolves through that same implicit cast |

The `bpchar -> varchar` exclusion is worth recording as a reasoning error,
not just a code fix. The rtrim arm added in slice 3 deliberately held varchar
out, with a comment saying upstream's bpchar->varchar "is a separate entry".
It IS a separate `pg_cast.dat` entry — but it names the same function. The
inference "separate entry, therefore separate behaviour" was never checked
against the catalog, and reading the catalog is what refuted it.

### Which failure was dangerous

`select_having` and `select_implicit` rendered a wrong column WIDTH with
correct values. `union` was different in kind: `SELECT CAST(f1 AS char(4))
FROM VARCHAR_TBL UNION SELECT f1 FROM CHAR_TBL` stopped de-duplicating,
because a padded and an unpadded image of one value compare unequal, so every
row came back twice. A type whose producers disagree about its physical form
makes every equality over it unreliable — dedup, hash join, grouping, index
lookup.

### A test-design rule this produced

`octet_length` is one of the `PadBpchar` RE-PADDING render callers. A
cast-padding test written on it reports the padded width **whether or not the
producer padded** — the first version of the new test did exactly that and
passed with the fix disabled (verified, not suspected). The valid witness has
to observe the raw image; the pinned test uses UNION de-duplication, which
compares the cast value against the stored value and yields one row only if
the two images are byte-identical.

This extends the rule slices 1–3 arrived at — *sites that re-pad are safe,
consumers that read the stored image are dangerous* — from the code to the
tests. A re-padding function is exactly as blind inside an assertion as it is
in production.

### Still open

Every text function receiving a bpchar resolves through the same rtrim1 cast;
only the `lower`/`upper`/`initcap` family is fixed. Measured on live PG 18.3
against a `char(6)` holding 'ab', `initcap`, `replace`, `substr` and
string concatenation all strip. Filed as `bpchar-text-function-class` with a
ledger row. No corpus case currently catches a further instance — the full
upstream regress suite is green apart from `partition_aggregate`, which is an
unrelated partitionwise-aggregation gap.

## The class, closed by measurement (2026-09-22)

The follow-on above fixed `lower`/`upper`/`initcap` and left the general
question open: which other text functions receiving a `char(n)` were still
reading the padded image? That was answered by measuring, not by reading the
source — one query computing `length(f('ab'::char(6)))` for 22 candidate
functions, run against the PG 18.3 reference and a private goopg scratch,
then diffed. 13 divergences, 9 already correct.

### The rule is not uniform, and assuming it was would have added bugs

| family | behaviour on a `char(n)` arg | why |
|---|---|---|
| declared `text` (`repeat`, `ltrim`, `replace`, `left`, `reverse`, …) | **strips** | resolves through the implicit bpchar→text cast, `rtrim1` |
| variadic `any` (`concat`, `concat_ws`, `format` `%s`) | **keeps the padding** | goes through the type's OUTPUT function, not a cast |

Measured on PG 18.3 against a `char(6)` holding `'ab'`: `concat` → 7,
`concat_ws` → 8, `format('%s', …)` → 6. goopg already matched upstream on
all three. They are now pinned in the test **as non-stripping**, so a later
loop cannot "fix" them into a consistency upstream does not have. Had the
class been closed by applying one rule everywhere — the obvious reading of
"text functions strip" — those three would have become new divergences.

### A sibling-path miss the audit caught

`length` was fixed in slice 1. Its aliases `char_length` and
`character_length` are a **separate `case` in the same switch** and still
returned the padded width. Nothing in the corpus caught it; only the
enumeration did. This is the Hard-won Rule #2 shape at its smallest scale —
two spellings of one upstream function, fixed apart.

### Implementation note

`coerceBpcharArgDatum` normalises the evaluated argument **once** per
function, instead of rewriting every `s.StringValue()` inside each body.
`regexp_replace` alone reads its subject four times; patching call-by-call
would have stripped in some branches and not others. Non-string datums pass
through untouched, so the bytea branches of `ltrim`/`reverse`/`btrim` are
unaffected.

### The one still open, and why it was not rushed

The concatenation **operator** remains divergent: PG gives 3 for
`length('ab'::char(6) || 'z')`, goopg gives 7. The 12 fixed cases are
`FuncCall`s whose bodies can see their argument *expression* — which is what
carries the declared `char(n)` type, since a padded datum is
indistinguishable from a text value that genuinely ends in spaces.
`evalBinary` receives only Datums.

The cheap fix is to coerce at the one production call site where the
`BinaryOp` still has its operands (`expr.go:1322`). That is a Rule #2 trap:
`exprnode.go:436` is the other evaluator's binary-op site and would keep the
old behaviour, producing a fast-path vs interpreted split on a *value*
question. Fix both or neither. Filed as `bpchar-concat-operator` with the
witness and both site references.

## The class is closed (2026-09-22): the concatenation operator, on both twins

The audit above left one divergence open — the concatenation operator — and
deferred it rather than reaching for the obvious fix. This section records
why, and what the shape of the eventual fix teaches.

PostgreSQL has no bpchar concatenation operator. The bpchar operand resolves
through the implicit bpchar→text cast (`rtrim1`), so the padding is stripped
*before* concatenation: on PG 18.3 a `char(6)` holding `'ab'` concatenated
with `'z'` has length 3, not 7.

### Why it could not be done with the other twelve

The rule needs the operand's **declared type**, never the datum: a
blank-padded bpchar image is indistinguishable from a text value that
genuinely ends in spaces. The twelve function cases are `FuncCall`s whose
bodies still have their argument expression. A binary operator does not —
and goopg evaluates one through **two** engines:

| evaluator | has the operand expression? |
|---|---|
| interpreted (`evalExprSlot`) | yes |
| compiled (`evalFastExpr`) | **no** — only Datums remain |

Coercing at the interpreted call site alone was the cheap fix, and it was
the trap: it would have left the compiled twin on the old behaviour, giving
a fast-path-vs-interpreted split on a **value** question. That is the
Hard-won Rule #2 failure shape exactly.

### The fix: capture the width where the expression still exists

`buildExprCtx`'s `BinaryOp` arm compiles `declaredBpcharTypmod` for each
side into the node payload (`payload[8:12]` / `[12:16]`, previously unused
in a 40-byte field). Compile time is the *last* point at which the operand
expression exists, so that is where the declared type has to be captured.
Both evaluators then call one shared helper, `concatOperandsAsText`: the
route to the width differs per engine, but the rule lives in one place and
cannot drift.

Rejected, recorded so it is not retried: threading operand expressions into
`evalBinary`. That signature has ~40 callers on a hot path, and the compiled
twin has no expression to pass anyway.

### The test design is the transferable part

The test drives **both** twins explicitly instead of going through SQL. A
SQL-level test exercises whichever evaluator the builder happens to pick,
and would therefore pass with one twin still broken — it cannot see the
defect it exists to prevent.

Non-vacuity was verified *per twin*: zeroing only the compiled payload fails
exactly the compiled assertions while the interpreted ones still pass (the
Rule #2 split, reproduced deliberately), and neutralising only the
interpreted call fails exactly the interpreted ones.

A third case pins that a genuine `text` operand whose value really does end
in spaces **keeps** them. That is what makes "strip by declared type" rather
than "inspect the datum" the only correct rule, and it would catch a future
`TrimRight`-the-operand shortcut.

With this, all 13 divergences the audit measured are closed.
