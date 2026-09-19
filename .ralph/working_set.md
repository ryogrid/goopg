(idle — nothing in flight)

Loop #30 note: M0119-0006bs landed via owner commit 09bde885a (their anchor
re-pin swept the staged index after the HOLD lifted) — verified at HEAD,
pushed. Open follow-up for the NEXT loop that touches internal/executor|planner:
bench/tpch/baseline-digests.txt is stale (pre-8-FK-reload, load-dependent) —
tpch-acceptance-arm will FAIL on data drift until it is re-captured
(tpch-runner -port 65433 -digest ... > new-baseline per its header).
