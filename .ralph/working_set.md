(idle — nothing in flight)

Last loop: M-NIGHTLY AI-20260811-014635-012 — the TPC-DS SF=1 bench cluster was
carrying pre-M0130-S11 nbtree indexes; REINDEXed, gate restored.

Filed this loop (unconditional M-NIGHTLY duty): all 12 items of nightly run
`20260811-014635` (sha `46103e4e`). Eleven are PARKED per banner — 2 isolation
testport failures and 9 regress cases with "output mismatch; normalization rules
need extension". **Re-run the regress repros at HEAD before investigating**: the
previous loop ran the FULL `TestPort_RegressSuite` GREEN at `c58650b7`, two
commits AFTER this nightly's sha, so they may all be stale.

Findings worth carrying:

- `btree: index contains corrupted page at block 0: special size N, want 16` on
  a long-lived cluster is **not corruption** — it is an index built before one
  of the three REINDEX-required M0130-S11 format flips. Fix is
  `bench/reindex_cluster.sh <port> [db]` (new this loop; 24 TPC-DS PKs in 95 s).
- The trap is which cluster you REINDEX. 2026-08-10 remediated **SF=0.5**
  (what the fast gate sweeps); the nightly clones **SF=1**. Whenever a
  REINDEX-required change lands, walk the whole port table in CLAUDE.md.
- The reason both incidents got all the way to a nightly: every guarding
  spotcheck is a seq-scan plan (`tpch-spotcheck.sh` Q12/Q13, `stage-tpcds.sh`
  Q3/Q98), so it passes green on a cluster whose every index is unreadable.
  Detection is filed as a ledger row, not built.
- TPC-H (65433, db `tpch`) is still un-REINDEXed — `REINDEX` inside db `tpch`
  hits the same per-DB catalog scoping gap the ledger records for ANALYZE.

Banner state (re-read this loop): M-NIGHTLY filing done; M0130 fully checked;
banner then falls through to M0119, then M0122.

Next loop: per banner, M0119-0006. Fresh candidates from the last slice: an
end-to-end subscriber round-trip over a publication on an array column
(`internal/testport`), TOASTed arrays in logical decoding, multi-dimensional /
NULL-element arrays (needs a WRITER first). Older: date/time array elements,
array SLICES `a[1:2]` (rejected by the LEXER), `interval[]` refused by
`decodeArrayKeyElemText`, posting-list duplicate coverage in the checkunique
tier, `box`/`int4range` key encodings, the whole-database pg_amcheck run.

Gates: units PASS; TPC-DS SF=1 six previously-ERRORing queries (q1 q6 q7 q9 q13
q91) return rows; SF=0.5 re-verified green. No Go code changed this loop (shell
script + docs + trackers), so tpch-spotcheck / SF0.5 sweep were not re-run.

In-flight: none
