# M0134-0001 P4 — `bytea_output = 'escape'` is silently ignored

**Status:** draft → accepted (2026-08-17)
**Task:** M0134-0001 (`aggregates.sql`), slice **S12**
**Case gate:** `scripts/pg-regress-runner.sh aggregates`

## Discovery

Filed from the S10-residual re-scoping done 2026-08-17
(`tmp/ralph-handoffs/m0134-0001-s10-scope/report.md`). That scoping overturned the
previous loop's claim that all 30 residual `aggregates.diff` hunks were
parallel-query plan shapes: only **5** are. Hunk 18 (diff line 533) is an isolated,
previously-unledgered bug with no relation to aggregation at all:

```sql
set bytea_output = 'escape';
```

goopg accepts the `SET` (the GUC is registered) and then **ignores it** — every
bytea value is still rendered in hex (`\x...`). PG renders the traditional escape
format. The GUC entry exists at `internal/utils/misc/defaults.go:788`
(`Name: "bytea_output", Type: TypeEnum, BootVal: "hex"`) but **no code reads it**;
`internal/executor/bytea.go` only ever defined `byteaOutHex`.

This is a user-visible output-format divergence on a stable, well-specified GUC,
so it is worth fixing on its own merits independently of `aggregates.sql`.

## PG oracle

`postgres/src/backend/utils/adt/varlena.c:397 byteaout()`. The escape branch:

- byte `== '\\'` → emit `\\` (two backslashes)
- byte `< 0x20 || > 0x7e` (i.e. non-printable, compared **as unsigned**) →
  emit `\nnn`, a backslash plus exactly three octal digits, low digit first from
  `val & 07`, then `val & 07`, then `val & 03` for the top digit (so the full
  0–255 range maps to `\000`–`\377`)
- everything else → the byte verbatim

The hex branch is `\x` followed by `hex_encode`, which is what goopg already does.
The GUC is `bytea_output`, enum `{hex, escape}`, default `hex`
(`postgres/src/backend/utils/misc/guc_tables.c`; user docs
`postgres/official_docs_in_md/` runtime-config-client, "Statement Behavior").

Note the unsigned comparison: a naive Go implementation over `byte` is already
unsigned and therefore correct, but a port that reads through `int8`/`rune` would
sign-extend and mis-encode `0x80`–`0xff`. The three-digit octal is fixed-width —
never variable-length.

## Design

Mirror the **DateStyle precedent already proven in-tree**, rather than inventing a
new mechanism. `date_out`/`timestamp_out` face exactly this problem (a leaf
formatter in `internal/utils/adt/...` that must observe a session GUC it cannot
reach), and goopg solves it by reading the setting at the executor boundary via
`ctx.GetSetting("datestyle")` and threading the resolved style down into the leaf
(`internal/executor/codec_array.go:317`, `internal/executor/copy.go:69`).

Accordingly:

1. **Leaf encoder.** Add `array.ByteaOutEscape(b []byte) string` beside the
   existing `array.ByteaOutHex` (`internal/utils/adt/array/pgarray.go:424`), a
   direct port of the `byteaout` escape branch. Keep both leaves pure and
   session-free.
2. **Mode selection at the executor boundary.** Resolve the GUC once per render
   path with `ctx.GetSetting("bytea_output")`, defaulting to hex on absent or
   unrecognised values (PG's enum validation rejects bad values at `SET` time, so
   the output path never needs to error). Thread the resolved mode into the leaf
   as an explicit parameter — never a package global, which would be wrong under
   goopg's concurrent per-session execution.
3. **All renderer siblings change together** (Hard-won Rule #2). The known
   scalar/array/COPY renderers are listed in the slice scope below; the
   implementer must prove the list is complete rather than assume it.

Deliberately **not** in scope: `byteain` (input). PG's `byteain` accepts both the
hex and escape forms unconditionally regardless of the GUC — input is
self-describing — so the decode twin genuinely needs no change. This is stated
here so a later reader does not mistake the asymmetry for an oversight.

## Verification

- Guard tests asserting the PG-exact escape rendering, including the boundary
  bytes `0x00`, `0x1f`, `0x20`, `0x7e`, `0x7f`, `0x80`, `0xff`, a literal
  backslash, and the empty value — expectations captured from PG 18.3, not
  transcribed by hand.
- A guard proving the GUC is **per-session**, not process-global.
- Round-trip: `byteain(byteaOutEscape(b)) == b`.
- Case gate: `scripts/pg-regress-runner.sh aggregates` — hunk 18 (line 533)
  disappears and no new hunks appear.

## Outcome (landed 2026-08-17)

Implemented as designed; the escape encoder was verified byte-for-byte against a
live PG 18.3 capture for `0x00`/`0x1f`/`0x20`/`0x7e`/`0x7f`/`0x80`/`0xff`/`0x5c`
and the empty value, and an adversarial review confirmed agreement across all 256
byte values (PG reads through a signed `char` and compensates with `DIG(val & 03)`;
Go's unsigned `byte` with `c>>6` yields the same 0–3, so there is no
sign-extension divergence).

**The sibling sweep was the substance of this slice, not the encoder.** Three of
the five renderer sites were unreachable from a grep for `byteaOutHex`, and the
last one surfaced only by *running* the case gate:

1. `internal/postmaster/dispatch.go appendTypedCellText` — its own inline hex
   loop; shared by the simple and extended query protocols.
2. `internal/executor/copy_text.go datumToCopyText` — **no bytea case at all**;
   the default `KindBytes` arm wrote raw unencoded bytes into the COPY TEXT/CSV
   stream. A pre-existing correctness bug larger than hex-vs-escape (HEAD's output
   did not even round-trip — `byteaIn` rejects it). Now PG-byte-identical in both
   formats and both modes, with COPY's own backslash-doubling layer confirmed
   against `copyto.c CopyAttributeOutText`.
3. `internal/executor/operators_join_agg.go` `string_agg` — a third independent
   inline hex encoder, keyed on `call.Name`/`arg.Kind` rather than a type name.

**A prediction this doc got wrong:** the "Verification" section expected hunk 18
of `aggregates.diff` to close. It did not — the diff stayed at 1096 lines / 30
hunks, with hunk 18's content merely changing from hex to escape. The blocker is
independent of `bytea_output`: `string_agg(x::text::bytea, ',')` passes an
untyped literal delimiter that `accumAgg` drops because it is `KindString`, not
`KindBytes`. That is pre-existing on HEAD under hex mode, so it is not a
regression, and it is recorded in `.ralph/deferral_ledger.md` (2026-08-17)
together with a second row for five further sites (`concat`, `format('%s',…)`,
`quote_literal`, `array_agg`, `ARRAY[…::bytea]`) that bypass the bytea output
function entirely in *both* modes.

**Regression caught in review:** the first cut of the new COPY bytea arm required
`KindBytes` and hard-errored otherwise, breaking
`COPY (SELECT string_agg(b,','::bytea) FROM zs) TO STDOUT` — a query that works
at HEAD and matches PG. `string_agg` over bytea returns a `KindString` datum while
advertising column type bytea, so the arm now renders `KindBytes` and passes
`KindString` through verbatim, mirroring `dispatch.go`. Guarded by
`TestCopyByteaAcceptsStringAggResult` (text + csv subtests), verified FAIL-pre /
PASS-post.

**Seam note:** `byteaOutHex`/`byteaOutEscape` were deleted after the sweep left
them with no non-test callers. `byteaOutMode` is the sole correct entry point for
any new bytea-output call site — a GUC-blind wrapper sitting next to it is exactly
how a sixth divergent site would be born.

## Cross-case leverage

This is an output-encoder fix, so every regress case that sets `bytea_output` or
prints a bytea inherits it. The regress-runner's `normalise_output()` does not
collapse these bytes, so unlike the S11 indentation gap this divergence is fully
visible to the gate.
