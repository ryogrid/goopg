Task: M0110-0003 (pg_amcheck) — landed the page-structural CORE of amcheck's
verify_heapam() as a standalone engine, engine-first (SQL surface deferred to a
clean tree). This is the keystone blocker for the 4 deferred pg_amcheck TAP
tests (AC-002: 002_nonesuch/003_check/004_verify_heapam/005_opclass_damage).

Files (all NEW — none touch the contaminated gen-column WIP files):
  - internal/amcheck/verify_heapam.go — VerifyHeapPage(p storage.Page) []Report.
    Tier-1 only: line-pointer bounds/alignment, redirect-target validity,
    tuple-header t_hoff consistency. Messages mirror verify_heapam.c verbatim.
  - internal/amcheck/verify_heapam_test.go — 11 unit tests (clean→0 reports;
    each corruption→exact upstream message). All PASS (0.003s).
  - docs/design/0110-0005-verify-heapam-engine.md (new) + README.md index row.
  - fix_plan.md M0110-0003 progress note + deferral_ledger.md line.

Key symbols: amcheck.VerifyHeapPage, checkTupleHeader; storage primitives used:
  PageGetItemID / PageLinePointerCount / IsNew / HeapNattsMask / HeapHasNull /
  SizeOfHeapTupleHeaderData / ItemID{Unused,Normal,Redirect,Dead}.

Gates run: go test ./internal/amcheck (11 PASS), gofmt/vet clean,
  go build ./... clean, make ralph-state-guard OK (had to restore
  .ralph/progress.json running→in_progress to match status.json; a stale
  loop-#50 "completed" write had desynced it).

Next step (DEFERRED — needs a CLEAN working tree; current tree carries another
  session's uncommitted gen-column/partition WIP across parser/planner/executor/
  catalog/analyzer/mvcc + server/dispatch.go): wire the SQL surface on top of the
  engine — (1) CREATE EXTENSION amcheck (parser + pg_extension row + pg_proc
  registration of verify_heapam/bt_index_check/bt_index_parent_check), (2) the
  verify_heapam(regclass,…) SRF that walks a relation's blocks through
  VerifyHeapPage emitting one row per Report — then port 002_nonesuch.pl (its only
  actually-checked relation is a clean pg_catalog.pg_class, so the tier-1 engine
  suffices for the exit-0 path). HOT/MVCC tiers needed only for 003/004's
  deliberately-corrupt fixtures.

⚠ TREE NOTE (still true): a SEPARATE manual session has uncommitted WIP across
internal/{executor,planner,catalog,analyzer,parser,mvcc}/ + server/dispatch.go +
untracked gen_override test files + postgres/ + validate-ralph-state. NOT
ralph's — stage only your own files (git add <paths>), never `git add -A`.

Other OPEN tasks (all blocked on big features): M0095-0003 (WAL streaming -X
stream), M0110-0001 (pg_dump 002+ catalog parity), M0110-0002 (pg_waldump 002 /
index AMs hash/gin/gist/spgist/brin).
</content>
