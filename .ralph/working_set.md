Task: M0110-0003 (pg_amcheck port) — SQL surface still HARD-BLOCKED, but the
amcheck ENGINE is NOT exhausted. This loop landed the heap xmin numeric-bounds
tier (committed). The MVCC tier still has uncontaminated runway (xmax bounds +
multixact) for future blocked loops.

LANDED THIS LOOP (loop #66 / driver #23, 2026-06-14):
- internal/amcheck/verify_heapam.go: new `checkXminBounds` + call site in
  verifyHeapPage. Ports verify_heapam.c:check_tuple_visibility's
  XID_IN_FUTURE / XID_PRECEDES_CLUSTERMIN / XID_PRECEDES_RELMIN arms (driven by
  get_xid_status bounds). Consumes the previously declared-but-unused RelDesc
  fields NextXid/OldestXid/RelFrozenXid. Gated on NextXid!=0 (unset sentinel →
  tier off → page-bytes-only callers byte-for-byte unchanged).
- internal/amcheck/verify_heapam_xminbounds_test.go: 8 tests (all PASS).
- docs/design/0110-0005 + docs/design/README.md: tier documented + indexed.
- .ralph/fix_plan.md (loop #66 note), deferral_ledger.md (productive-loop line).
Key symbols: checkXminBounds, headerXmin, RelDesc{NextXid,OldestXid,RelFrozenXid},
verifyHeapPage. Upstream: contrib/amcheck/verify_heapam.c get_xid_status (2111),
check_tuple_visibility (1112).

Gates run: `go test ./internal/amcheck ./internal/access/btree` PASS;
gofmt -l + go vet ./internal/amcheck clean; make ralph-state-guard consistent.
(No TPC-H gate — amcheck is its own package, no executor/planner code touched.)

STATE: foreign gen-column WIP STILL frozen at 2026-06-13 14:28 across
internal/{parser,planner,executor,catalog,analyzer,mvcc}/ + server/dispatch.go +
untracked gen_override test files. Owning session `claude --resume ec98936f`
ALIVE. DO NOT touch/stash/commit it — a HUMAN must clear it.

Next step (while tree stays blocked): port the NEXT uncontaminated engine slice —
the xmax numeric-bounds + multixact-membership checks of check_tuple_visibility
(verify_heapam.c after the xmin arms), same RelDesc-driven pattern, in
verify_heapam.go only. Once the tree is CLEAN: wire SQL surface slice S1 from
docs/design/0110-0008 (CREATE EXTENSION amcheck + pg_extension row), then S2,
then port 002_nonesuch.pl.
