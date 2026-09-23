(idle — nothing in flight)

Last loop (ralph2 #7): M-NIGHTLY isolation diffs DONE (ffe1d020f):
batch-filter read-ahead broke LockRows' currentTID on partitioned FOR UPDATE
(bisected d6e42a7f7); intra-grant-inplace-db now runs in isolation_regression.
Banner: items 3/6/7 [!] (owner decisions pending: M0145-0001, M0141-S7,
M0140-0007); next per item 10 = remaining M-NIGHTLY, then M0119 → M0122 →
M0131 → M0134 → M0135/M0136 → M0095/M0110.
