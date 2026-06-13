Task: M0110-0003 (pg_amcheck) — standalone `internal/amcheck` verify_heapam()
engine. Engine-first/wire-later continues; SQL surface still deferred (needs a
CLEAN tree). This loop added the **relation-natts tier**.

Landed loop #54 (all in NEW files — zero contaminated files touched):
  - internal/amcheck/verify_heapam.go: new `RelDesc{Natts}` type + entry point
    `VerifyHeapPageWithRel(p, blkno, rel)` (VerifyHeapPage now a thin zero-RelDesc
    wrapper). `checkTupleHeader` now returns bool (header clean enough to
    continue). First pass emits `number of attributes %d exceeds maximum expected
    for table %d` (verify_heapam.c:1942 check_tuple) when natts > rel.Natts,
    gated on headerOK + rel.Natts>0. Package doc updated.
  - internal/amcheck/verify_heapam_test.go: +4 tests (setNatts helper; exceeds,
    within/boundary, disabled-without-rel, suppressed-when-header-corrupt). 27 PASS.
  - docs/design/0110-0005-verify-heapam-engine.md: new relation-natts tier
    section + API (RelDesc/VerifyHeapPageWithRel) + testing; recorded
    check_tuple_attribute as goopg-DIVERGENT (not just deferred).
  - fix_plan.md loop-#54 PROGRESS note + deferral_ledger.md line.

KEY divergences/decisions (load-bearing):
  - natts is page-structural (t_infomask2 & HeapNattsMask); the ONLY relation
    metadata the check needs is the column count → faithful to goopg.
  - Visibility gate skipped (no clog): applied to every header-clean tuple. Safe
    for goopg — natts>table is structural corruption regardless of visibility,
    and goopg drops columns logically (attisdropped), never shrinking natts.
  - check_tuple_attribute NOT ported and NOT just deferred: it decodes PG's
    on-disk varlena/varatt_external TOAST format; goopg's TOAST is a separate
    chunk relation (internal/executor/toast.go) — a verbatim port would
    false-positive. A goopg-faithful version is a reimplementation, not a port.

Gates run: go test ./internal/amcheck (27 PASS), gofmt -l clean, go vet clean,
go build ./internal/amcheck clean, make ralph-state-guard OK (reconciled
progress.json running/in_progress + timestamp skew).

Next step (DEFERRED — needs a CLEAN tree): the SQL surface — (1) CREATE
EXTENSION amcheck (parser + pg_extension row + pg_proc registration of
verify_heapam/bt_index_check/bt_index_parent_check), (2) the
verify_heapam(regclass,…) SRF walking a relation's blocks through
VerifyHeapPageWithRel (pass RelDesc{Natts: relColumnCount} per page) emitting
one row per Report — then port 002_nonesuch.pl (its only checked relation is a
clean pg_catalog.pg_class, so tier-1 suffices).

⚠ TREE NOTE (still true): a SEPARATE manual session has uncommitted WIP across
internal/{executor,planner,catalog,analyzer,parser,mvcc}/ + server/dispatch.go +
cmd/goopg/main_test.go + untracked gen_override test files + postgres/ +
validate-ralph-state. NOT ralph's — stage only your own files (git add <paths>),
never `git add -A`.

Other OPEN tasks (all blocked on big features): M0095-0003 (WAL streaming -X
stream), M0110-0001 (pg_dump 002+ catalog parity), M0110-0002 (pg_waldump 002 /
index AMs hash/gin/gist/spgist/brin).
