# M0146-0011 re-census of the lateral seam-decline family (2026-10-05)

Source: bench/tpcds/runtime_goopg/tpcds-results-sf025/flow-convergence.tsv (the SF0.25 sweep's flow-convergence channel).

## Last runs that recorded a lateral decline (all legacy knob arm)
2026-09-24T11:24:40.103507+00:00	sweep-20260924-202143.txt-knob	207	0	inf	lateral=1,leaf-count=27,outer-spine=2
2026-09-24T11:57:57.137658+00:00	sweep-20260924-205439.txt-knob	207	0	inf	lateral=1,leaf-count=27,outer-spine=2
2026-09-24T12:29:46.819197+00:00	sweep-20260924-212633.txt-knob	207	0	inf	lateral=1,leaf-count=27,outer-spine=2

## Default (jointree) arm since the cutover: first and last five runs
2026-09-24T13:22:38.806519+00:00	sweep-20260924-221958.txt	0	23	0.00	leaf-count=2
2026-10-04T14:36:25.289243+00:00	sweep-20261004-232748.txt	0	33	0.00	leaf-count=2
2026-10-04T15:26:46.760432+00:00	sweep-20261005-001804.txt	0	33	0.00	leaf-count=2
2026-10-04T16:36:43.693847+00:00	sweep-20261005-011014.txt	0	33	0.00	leaf-count=2
2026-10-04T16:49:20.855383+00:00	sweep-20261005-013718.txt	0	33	0.00	leaf-count=2
2026-10-04T17:25:19.599080+00:00	sweep-20261005-021429.txt	0	33	0.00	leaf-count=2

## Q30 / Q68 first divergence (routing-20261004b)
Q30 depth=5 [join-method] under Nested Loop: PG Nested Loop Inner | goopg Hash Join Inner
Q68 depth=4 [scan-type] under Nested Loop: PG Index Scan customer_pkey on customer | goopg Bitmap Heap Scan on customer
Q30 depth=4 [scan-type] under Nested Loop: PG Index Scan customer_pkey on customer | goopg Bitmap Heap Scan on customer
Q68 depth=3 [join-method] under Nested Loop: PG Nested Loop Inner | goopg Hash Join Inner
