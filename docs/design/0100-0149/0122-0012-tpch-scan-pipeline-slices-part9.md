# M0122-0012 — scan pipeline slices, part 9 (TPC-DS executor gaps, continued)

Continued from [part 8](0122-0012-tpch-scan-pipeline-slices-part8.md) (slices 80–89), which was split before the
800-line limit.

**Measurement method.** As in part 8: the SF0.25 clone `tmp/q64-clone`, with instructions:u per query
(`tmp/ds-instr.sh`) as the instrument and allocation volume (`tmp/ds-alloc.sh`) alongside it.

**Reference timings.** Gaps are ranked against release PG 18.3, whose per-query medians are in
`analysis/timing-after-planprity-match/data/tpcds-sf025-pgrel.tsv`.

Wall-clock ratios from one long warm sweep are unreliable for queries that follow a heavy one (part 8, slice 89).
Warm each query on its own, or rank by instructions.

## Slice 90 — the plain-string test and the string read inline at the comparison

**Target.** Q81, and its sibling Q30, still spent their time in the slice-89 lane. Their correlated SubPlan compares
`ctr1.ctr_state = ctr2.ctr_state` for every inner CTE row, as PG's does. That made the lane's two support calls the
largest costs in Q81:

- `indexbytebody` at 26 %: `plainCompareString`'s `strings.IndexByte` on a two-letter state code, where the call into
  the vector routine costs more than the scan;
- `Datum.StringValue` at 6.5 %: its arena arm puts it over the inlining budget (cost 143 against 80), so every read is
  a call.

**Change** (`exprnode.go`).

- **`plainCompareString`.** It tests the leading '(' or '{' and the 32-byte cut first, then scans for '/' with a byte
  loop, and now inlines. The result is the same as the `IndexByte` form: the string is under 32 bytes whenever the
  loop runs.
- **`stringValueInline(d)`.** It reads an owned string straight from `Buf`; `unsafe.String(unsafe.SliceData(Buf),
  len(Buf))` is "" for an empty or nil buffer, as `StringValue` returns. An arena-backed string calls `StringValue`. It
  inlines (cost under 80) and replaces the two `StringValue` calls in both plain-string compares: the slice-89 lane and
  `evalFastBinaryApply`'s branch.

**Why the plain test stays.** For a character-string pair (payload bit 4), most non-plain strings compare as text on
the general path too:

- `textShapeAmbiguous` sends shape-alike pairs to `binaryTextComparison`, which is `strings.Compare` after the bpchar
  trim.

The exception is a pair that is not shape-ambiguous but has one side carrying a '/':

- it reaches `evalPgLSNBinary` on the untrimmed values;
- a padded bpchar that parses as a pg\_lsn there would compare numerically.

So the guard is required, and only its cost changed.

**Pins** (in `string_compare_inplace_test.go`).

- `TestPlainCompareStringMatchesIndexByteForm` checks the byte loop against the `IndexByte` form, with a '/' at every
  position, leading and non-leading brackets, and lengths 30 to 33. Starting the loop at index 1 fails it.
- `TestStringValueInlineMatchesStringValue` covers owned, empty, nil-buffer and arena-backed strings. Returning "" for
  the arena arm fails it.
- Slice 89's `TestStringCompareInPlaceMatchesInterpreted` still passes.

**Measured** against `e8bf90456` (instructions:u per run):

| query | before | after | change |
|---|---|---|---|
| Q81 | 36.90 G | 31.34 G | −15.1 % |
| Q30 | 8.45 G | 7.24 G | −14.3 % |
| Q59 | 12.09 G | 11.86 G | −1.9 % |
| Q2 | 8.93 G | 8.88 G | −0.6 % |

Two steps make up Q81's −15.1 %: the byte loop alone was −9.1 %, and the inlined read another −3.4 % before its last
budget trim.

Values are identical:

- sf025 PASS=99 (61 ck) with identical plan shapes, and PASS again with the deform poison armed;
- the executor package passes with the poison armed;
- acceptance 24 MATCH;
- spotcheck PASS;
- RegressSuite PASS;
- TestPort\_Isolation PASS.
