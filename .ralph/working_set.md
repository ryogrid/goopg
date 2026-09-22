(idle — nothing in flight)

M0145-0021 is COMPLETE and committed: the two-scale fire-set gate template
is green at SF0.25 and SF1 (same derived 24-id fire set, all PASS in both
arms, `introduced=none unchanged=none missing=none`, exit 0).

Carry-forward for whoever runs this harness next:
- `FIRESET_BATCH_SIZE=1` is a trap. The execution arm starts its private
  clone server ONCE per invocation and then walks every id in
  `FIRESET_QUERIES`, so batch 8 pays one clone+start for eight queries.
  Loops #92-#99 used batch 1 and advanced one id per loop; batch 8/9
  finished both remaining halves in minutes.
- SF1 is cheap: both arms' plan A/B (two 3.3GB offline clones + the PG
  reference arm, 99 EXPLAINs each) took 89 s; the 24 fires ran in three
  batches of 1m59s-3m59s.
- The SF1 source `bench/tpcds/runtime_goopg/data` must be DOWN (no
  postmaster.pid) or the clone step exits 3.
- Follow-ups filed: M0145-0021a (enforcement — needs an owner scope call)
  and M0145-0021b (TPC-H corpus; the tpch branch returns before the
  FIRESET block).

Next loop: re-read the `## Current Priority` banner and select from item 3's
M0145 chain (0009 → 0019 → 0020 sequence per the 2026-09-22 owner GO).
