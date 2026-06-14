Task: M0110-0003 (pg_amcheck) — amcheck verify engines in `internal/amcheck`.
Engine-first/wire-later. Loop #60 ported the **Bloom filter primitive** for the
last remaining B-tree tier, `heapallindexed`. The B-tree page-bytes engine is now
complete; what remains is heapallindexed (needs heap scan + index TupleDesc) and
the SQL surface — both blocked on a CLEAN tree.

Landed loop #60 (all NEW/additive amcheck code — ZERO contaminated files):
  - internal/amcheck/bloomfilter.go: faithful port of
    postgres/src/backend/lib/bloomfilter.c (+ .h). Upstream-verbatim
    `bloomCreate`/`bloomAddElement`/`bloomLacksElement`/`bloomPropBitsSet`/
    `myBloomPower`/`optimalK`/`kHashesValues`/`modM`, 1MB-floor/512MB(2^32-bit)-
    cap power-of-two sizing, enhanced double hashing. ONE documented divergence:
    the hash. Upstream seeds from `hash_any_extended` (Jenkins lookup3, mirrored
    as unexported `pgHashBytesExtended` in internal/executor/hash_partition.go);
    importing it would entangle amcheck with the contaminated executor pkg, and
    no-false-negatives holds for ANY shared hash, so the port uses a
    self-contained seeded FNV-1a + MurmurHash3 fmix64 finalizer (both 32-bit
    halves avalanched for the double-hashing split). All funcs unexported (same
    pkg as the future heapallindexed consumer); linter "unused" is expected.
  - internal/amcheck/bloomfilter_test.go: 8 tests (no-false-negatives [load-
    bearing], realistic-regime FP<5%, seed-distinguishes-FPs, sizing invariants,
    empty/var-length elems, myBloomPower/optimalK/modM helpers). NOTE: the 1MB
    floor over-provisions small sets (FP→0, saturation~0.11), so the FP-rate &
    seed tests size n=1M to reach the real ~0.5-density regime — do not shrink n.
  - docs/design/0110-0006-amcheck-bloom-filter.md + README index row; fix_plan
    loop-#60 PROGRESS; deferral_ledger loop-#60 line.

Gates run: go test ./internal/amcheck PASS (8 new + existing); go build ./... OK;
gofmt -l clean; go vet ./internal/amcheck clean. make ralph-state-guard (run
before status block).

Next step (BLOCKED on a clean tree): the `heapallindexed` scan that consumes the
filter — heap scan + per-heap-tuple index-tuple formation (needs the heap
relation + index TupleDesc) + bloomLacksElement probe — lands WITH the SQL
surface: CREATE EXTENSION amcheck + verify_heapam(regclass) SRF +
bt_index_check(regclass) wired on VerifyHeapPageWithRel / VerifyBtreePage /
VerifyBtreeItemOrder / VerifyBtreeLevelSiblingLinks / VerifyBtreeParentDownlinks,
then port 002_nonesuch.pl (promotes AC-002). Also: hash unification (substitute
shared Jenkins for bloomHash64 once it can leave internal/executor) —
distribution-only, no contract change.

⚠ TREE NOTE (STILL TRUE, STATIC since 2026-06-13 14:28 — now ~2 days): a SEPARATE
manual session's uncommitted gen-column WIP spans internal/{executor,planner,
catalog,analyzer,parser,mvcc}/ + server/dispatch.go + cmd/goopg/main_test.go +
untracked gen_override test files + postgres/ + validate-ralph-state. NOT ralph's
— stage only your own files (git add <paths>), never `git add -A`. Both remaining
amcheck slices (heapallindexed + SQL surface) are BLOCKED on this clearing.
STRONGLY consider surfacing to the user for a stash/commit decision — amcheck
cannot meaningfully progress past the page-bytes engine without a clean tree.

Other OPEN tasks (all blocked on big features): M0095-0003 (WAL streaming -X
stream), M0110-0001 (pg_dump 002+ catalog parity), M0110-0002 (pg_waldump 002 /
index AMs hash/gin/gist/spgist/brin).
