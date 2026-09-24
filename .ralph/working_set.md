(idle — nothing in flight)

This loop: M0145-0008h landed as 39751edc9 (comment fix 71b5b84d6). The
upstream agg_sort_order planner panic is fixed; the plan equals PG's.
Correction: the panic only dropped the SESSION (serveConn recovers it); the
server did not exit. Fixed in the fix_plan escalation and the commit message.
Found and filed M0145-0008m (WRONG RESULTS, S2 escalation): a PK-dependent
column reads NULL when grouping elects the index-ordered IOS input.
Reproduces at HEAD.
Disk: the fire-set / capture lanes leave 3.3 GB per-label SF1 clone dirs
(tmp/<label>-tpcds-sf1-{baseline,candidate}-data-tpcds-sf1). They filled the
disk mid-gate; 44 stale ones were deleted (145 GB). Delete your own labels'
clones after each fire-set run.

Next per the banner (item 3): M0145-0008i (multi-relation GROUP BY pruning),
0008j, 0008k, 0008l; 0008m awaits owner placement (S2). Then the
legacy-deletion slices and the M0146 continuation.
Parked: tmp/m0122-alter-system-wip.patch.
