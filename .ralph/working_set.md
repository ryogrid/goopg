(idle — nothing in flight)

M0131-S30.7 AND S30.8 FIXED and committed (loop #148). Root cause: the durable
CLOG was never attached to the transaction manager (`SetCLog` had zero
production callers) AND `SeesCommittedXID` skipped the CLOG for `xid < Xmin`,
so after crash recovery every correctly-Aborted in-flight XID read as
committed. `crashprobe30` run 1 now exact (-1498/-1498), first ever.

Next loop: re-read the `## Current Priority` banner in .ralph/fix_plan.md
(M-NIGHTLY filing first, as always). The obvious M0131 successor is the newly
filed **S30.1b** — `invalid page header` early end-of-WAL costing 6762 rows in
crashprobe30 run 2 (failing cluster preserved at /tmp/crashprobe30/run2,
lsn=117432305); it is the last thing standing between the S30 group and
`OVERALL: PASS`.
