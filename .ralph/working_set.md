# Working set — loop 17 (2026-09-27), CLOSED

Task: M0146-0005 slice 24 / M0146-0005w — `Subquery Scan` leaf wrapper
with setrefs-style triviality strip (witness TPC-DS Q8).

LANDED + PUSHED: see `git log` — `optimizer(M0146-0005w)` commit on
`plan-parity-with-pg-take2-ralph2`.

Final state:
- `optimizer.SubqueryScan{Alias,Child,schema,src}` — label-only,
  executor-transparent wrapper emitted by `planSubqueryRangeVar` on
  every leaf `is_simple_subquery` declines (appendrel UNION ALL stays
  inline). All generic walks taught it; EXPLAIN renders
  `Subquery Scan on <alias>` with the subplan nested beneath.
- `subqueryscan_strip.go`: `stripTrivialSubqueryScans` at `Plan()`'s
  tail ports setrefs.c `trivial_subqueryscan` — label survives iff
  consumed leaf-local positions ≠ full in-order identity or a Filter
  sits on the leaf. Scope bounding via `rtableScope.derivedSubtrees`
  (all FROM-subquery subtree roots, wrapped or not) + structural
  CTEScan/setop-arm boundaries; ref mapping by (Name, SourceTableIdx)
  — `ColumnRef.Index` is eval-context-global, NOT leaf-local.
- All four gate stamps PASS against the staged code_tree
  `d9a0e48cc7a0e7fd64e4d614d0740bf1d7969952d7b26ee86ae061b87c819532`:
  units, spotcheck (Q12=2/Q13=33), sweep 96 PASS/0 err, arm 24/24
  values, fireset `introduced=none` at both scales.
- Q8 renders PG's leaf structure: `Subquery Scan on a1` kept (subset
  consumption), v1/a2 stripped, HashSetOp direct under NL; 0 rows =
  oracle. Q8 left `jointree-search` for D3-partialpath at SF0.25.
- Residue (not this ticket): leaf-level quals PG would push via
  `subquery_push_qual` keep the label (Q34/Q73 reclassed into
  jt-search at both scales) — the kept label is faithful; the pairing
  loss is the missing-pushdown gap.
- Scratch: tmp/fireset-0005w (artifacts copied into
  analysis/m0146/m0146-0005/slice24/fireset/),
  tmp/arm-on-m0146-0005w.txt,
  tmp/m0146-0005v-data-tpcds-sf025 (:5533 server STOPPED; dir is a
  usable SF0.25 clone, ~3.3 GB, safe to delete).

Next loop: fix_plan banner order — next filed `[ ]` child of the
M0146-0005 family. Known adjacents this slice surfaced:
`subquery_push_qual` (outer-qual → subquery HAVING pushdown; Q34/Q73
witnesses) and M0146-0010 (Materialize wrappers — Q8's remaining
subtree delta under the NL).
