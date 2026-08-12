(idle — nothing in flight)

M0119-0006 42nd slice landed. Item stays UNCHECKED (standing slice-by-slice
cluster; 1 ledger row flipped to `resolved`, 4 new rows filed).

Selection note for the next loop: banner order re-verified. M-NIGHTLY filing done
(action-items.md still run `20260812-005501`, all four `## AI-` items already
filed — nothing new). M0131's two unchecked items are both formally closed
(S9 = closure bookkeeping, successor M0133 filed-not-promoted; S24 =
deferred-with-ledger), M0130 has zero unchecked, and M0095-0003 (the only other
hit in that line range) is explicitly "not actionable until logical decoding
lands". Fall-through lands on M0119-0006 again.

Landed this loop:
- `internal/pgarray/pgarray.go`: `OutputStyle` + `DefaultOutputStyle()` +
  `FormatDateElem`/`FormatTimestampElem`/`FormatTimestampTZElem` (they call
  `internal/config`'s formatters — the SCALAR path's own) + `RenderTextStyled`
  / `DecodeElemStyled`; old entry points kept as default-style wrappers.
- `internal/executor`: `arrayOutputStyle` + `colsHaveArray` (codec_array.go),
  `…Styled` variants in codec.go / operators_indexonly.go / btree_array_key.go,
  style resolved once in Open in seqScanOp / bitmapHeapScanOp / indexOnlyScanOp.
- Tests: `internal/pgarray/array_elem_output_style_test.go` (33 PG-18.3 oracle
  cells), `internal/executor/array_elem_output_style_test.go` (4 tests).
- Design `docs/design/0119-0006-array-element-output-style.md`, README row
  `0119-0006z`, fix_plan 42nd-slice note, 4 ledger rows.

Worth carrying:
- **A ledger row's stated BLOCKER can be stale, not just its suspects.** This
  row said "no tzdata lookup in a leaf package"; the 39th slice had already put
  one in `internal/config`, which `go list -deps` proves is a true leaf. Check
  the blocker against HEAD before accepting a row's framing — the REAL blocker
  (array text is fixed at heap-decode time, ~70 session-less call sites) was
  never written down.
- **Adding a `…Styled` variant beside an unchanged entry point** is how to
  thread new context through a function with ~70 call sites without churning
  them or their tests.
- Measuring the oracle beat trusting the row again: `date[]`/`timestamp[]`
  ignored DateStyle too (unfiled), while `time`/`timetz` correctly do not.

Gates: `internal/pgarray` + `internal/executor` + `internal/config` +
`internal/wal` PASS, `RALPH_PRECOMMIT_SCOPE=units` PASS, `TestPort_RegressSuite`
PASS (947 s — needs `-timeout 1800s`; the default 600 s timeout aborts it and
looks like a failure), `scripts/tpch-spotcheck.sh` PASS (Q12=2, Q13=35),
`make ralph-state-guard` OK. pgbench smoke via the commit hook.

In-flight: none.
