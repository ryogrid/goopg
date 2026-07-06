Task: M-NIGHTLY (AI-20260706-201855-001) — pgbench/nightly btree
keyLen-mismatch corruption. Loop 15 CONFIRMED (not just "consistent
with") that genuine on-disk pgbench_accounts HEAP tuple bytes are
physically present inside pgbench_accounts_pkey's B-TREE pages. See
deferral_ledger.md's 15th-consecutive-loop row (2026-07-07) and
fix_plan.md's "update #13" for full detail.

What this loop did: reconstructed update #12's forensic dump helper
(`internal/access/btree/parse_err_dump.go`, `maybeDumpPageOnParseErr`,
gated `GOOPG_BTREE_PARSE_ERR_DUMP=1`, wired into all 6 `perr != nil`
branches in btree.go) — and this time COMMITTED it instead of
reverting (env-gated, zero-risk when unset; loops 12 and 14 each had
to rebuild an equivalent tool from scratch, a real now-measured cost).
Reproduced fresh on an isolated port-5561 server (`pgbench -i -s 50`
once ~4min, then `pgbench -c 100 -j 20 -T 25 -P 5`, ~10s to failure),
captured 100 dumps.

Key finding (verified via full structural decode, not just a partial
byte match): decoding a corrupted line-pointer's full 37-byte raw
content as `internal/storage/heap.go`'s `HeapTupleHeader` layout gives
Xmin=9, Xmax=0, Xvac=0, CTID={InvalidBlockNumber,0} (exactly
`NewHeapTuple`'s pre-insert default), Infomask2 natts=4 (matches
pgbench_accounts's 4 columns), Hoff=24 (exactly
MAXALIGN(SizeOfHeapTupleHeaderData=23)) — 5 independent fields all
correct simultaneously. The data immediately following decodes as
aid=3706034 / bid=38, both in-range for scale=50. This rules out
coincidence: real pgbench_accounts heap tuple bytes are landing inside
the pkey index's on-disk btree pages. Also: the corrupted item's
offset+length place it entirely INSIDE the page's special/opaque
region (btSpecialOffset=7920..8192), meaning the page's pd_special was
wrong (heap-shaped, 8192) at write time, not just misread later.

Next step: audit `internal/executor/operators_storage.go` and
`operators_upsert.go`'s INSERT execution path end-to-end — these were
located (via grep for files calling both a heap mutation and
`btree.BTree` methods) as the files to read but NOT yet read in
detail. Look for where the heap-file vs. pkey-index-file
RelFileNode/storage handle are each obtained: are they two genuinely
independent derivations every time, or is there a route where a
per-statement/per-connection cached reference meant for the heap could
leak into the `bt.Insert` call (or vice versa) under concurrency? Then
re-run the cheap repro (`pgbench -i -s 50` once + `pgbench -c 100 -j
20 -T 25 -P 5`, pick a free port via `ss -ltn` first) with the now-KEPT
`parse_err_dump.go` tool (`GOOPG_BTREE_PARSE_ERR_DUMP=1`) plus new
executor-side logging of every `(heap RelOid, index RelOid, blk)`
triple passed into a storage-layer write during the same window, to
directly catch the moment a write meant for one relation lands on the
other's file.

Gates run this loop: `go build ./...` clean. Full
`go test ./internal/access/btree/...` package suite (2.0s) PASS after
the wiring change. `make ralph-state-guard`: run next, see status
block.

In-flight: none. Test server (`/tmp/goopg-loop15-data`, port 5561,
cgroup scope `goopg-loop15`) stopped and fully cleaned up; binary,
datadir, server log, and all `/tmp/btree-parse-err-*.dump` files
removed. No servers or background processes left running. Separate
live nightly CI batch (`ci/batch/run-nightly.sh`) and the protected
`goopg-wp.scope` on port 5544 were not touched.
