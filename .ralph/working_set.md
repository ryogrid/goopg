Task: M0119-0006-DATCONNLIMIT-DEFAULT (the CRITICAL bug found + deferred in the
prior loop). COMPLETE and committed this loop (commit cdda0fda).

Files: internal/catalog/catalog.go (`DatabaseConnLimit` now uses the map's
comma-ok lookup, returns -1 when unset instead of the Go zero-value 0; fixed
2 stale "0 = no limit" comments — struct field doc + function doc).
internal/server/database_exists_test.go (new
TestConnectNonSuperuserFirstConnectionUnlimitedDatabaseAccepted; had to read
frames in a loop until ErrorResponse/ReadyForQuery, NOT just the first frame,
since AuthenticationOk is always sent before the datconnlimit check runs).
.ralph/deferral_ledger.md (flipped the CRITICAL row to resolved).
.ralph/fix_plan.md (M0119-0006-DATCONNLIMIT-DEFAULT checked off).
docs/design/0110-0003-pg-amcheck-tap-port.md + docs/design/README.md (new
"Follow-up (2026-07-08)" section; M0119-0006's datconnlimit cluster now has
no known open residuals).

Key symbols: internal/catalog/catalog.go's DatabaseConnLimit (~4527);
internal/server/server.go's positive-datconnlimit check (~line 970, UNCHANGED
— the bug was purely in the catalog-layer default, this check's logic was
already correct once given the right default).

Findings: confirmed non-vacuous the hard way — first test draft (read only
the first frame, expect Authentication) passed even against the pre-fix
buggy code, because AuthenticationOk is unconditionally sent before the
positive-datconnlimit check executes; both accept and reject paths start
with the same first frame. Fixed by reading frames in a loop until
ErrorResponse (fail) or ReadyForQuery (pass), matching the pattern the
existing reject-path tests already use. Re-verified by reverting catalog.go
to pre-fix HEAD content and re-running: fails with the exact predicted 53300
"too many connections" FATAL; restored the fix and re-confirmed PASS x3.

Next step: pick the next M0119 item. Remaining open M0119 items (all larger
than a single loop, per fix_plan.md): M0119-0004 (pg_dump, blocked on per-DB
catalog isolation), M0119-0005 (needs hash/gin/gist/spgist/brin AMs, large),
M0119-0006's opclass-dispatch remainder (M0122-0001's still-open btree
opclass/comparator dispatch gap — see deferral ledger's 2026-07-07
M0122-0001 row), M0119-0007 (blocked on logical decoding). Also scan
.ralph/fix_plan.md fresh for any other URGENT/high-priority markers before
picking one of these larger items, since a smaller well-scoped item may have
been added since this loop started.

Gates run: go build ./... clean. go test ./internal/server/...
./internal/catalog/... ./internal/activity/... PASS (x3 repeat, confirmed no
flake). go test $(go list ./internal/... minus /testport and
/testutil/tpch, both excluded per project convention — slow/external-binary
suites) ALL PASS. scripts/tpch-spotcheck.sh PASS (Q12=2/Q13=33).
RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh PASS (0 failed,
all 3 workloads) — run standalone AND again via .githooks/pre-commit at
commit time. make ralph-state-guard: 1 benign issue auto-repaired (same
recurring status/progress clean-exit-vs-in_progress reconciliation as every
prior loop).

In-flight: none. All debug scaffolding (temporary fmt.Println in server.go,
a throwaway internal/catalog/zz_debug_test.go, /tmp/catalog.go.fixed,
/tmp/limit_check_test.go, /tmp/catconnlimit.patch) was removed/reverted
before this loop ended — `git diff internal/server/server.go` confirmed
clean before commit.
