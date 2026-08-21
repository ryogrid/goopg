Task: M0134-0070 (strings.sql) — regress-sql `failed`. This loop fixed a
higher-severity bug discovered while sizing 0070's remaining diff buckets:
the DataRow empty-string-vs-NULL wire-encoding bug (unconditional under
extended protocol, state-dependent under simple query). strings.sql itself
still `failed` (2624-line diff pre-fix, not yet re-measured post-fix).

Files this loop: `internal/postmaster/dispatch.go` (SELECT result loop
~3046, FETCH-from-cursor loop ~3813 — coerce nil post-render slice to
`[]byte{}` when Datum non-null), `internal/postmaster/dispatch_extended.go`
(Bind/Execute row builder ~521/525, same coercion), new test
`internal/postmaster/datarow_empty_string_test.go` (4 cases). Also
`.ralph/fix_plan.md` (M0134-0070 entry) and `.ralph/deferral_ledger.md`
(flipped the prior "discovered" row to `resolved`, appended a new row with
full fix detail).

Key symbols: the 4 DataRow-cell-building call sites above. `d.IsNull()`
branches untouched (still emit literal `nil` for the true NULL sentinel).
`internal/libpq/frame.go`'s `PutDataRowScratch`/`WriteDataRow` untouched
(correct by contract — nil is NULL there).

Hypothesis/Findings: root cause confirmed — Go's `nilSlice[0:0]` and
`append(nil, ...zero bytes...)` both stay `nil`, indistinguishable from the
NULL sentinel at the DataRow-cell layer. Extended protocol (Bind/Execute)
was UNCONDITIONALLY broken (every empty non-null cell, every connection —
affects any JDBC/psycopg-style prepared-statement client). Simple-query was
broken only on the very first non-null-empty cell rendered onto a
connection's still-nil scratch buffer. Researcher scanned
`internal/postmaster/query.go`, `internal/backup/basebackup.go`,
`internal/replication/walsender.go` for a 5th call site — those build rows
from Go literals directly (not `Datum.AppendValueText`), likely unaffected,
not exhaustively audited.

Next step: re-run `scripts/pg-regress-runner.sh --verbose strings` to get
strings.sql's post-fix diff line count (several fixture lines depended on
empty-string results, so it should shrink from 2624), then continue sizing
M0134-0070's remaining buckets — start with the string-literal continuation
gap (small/contained); REFACTOR-tier buckets (Unicode-escape literals,
POSITION/OVERLAY/LIKE ESCAPE/SIMILAR TO grammar, regexp_count/like/instr/
substr family, regexp_replace backreferences, regexp_matches 'g' flag,
regexp_split_to_table) are multi-file and should be split into their own
milestone-scale slices, not attempted in one round.

Gates run this loop: `go build ./...` PASS; `go test
./internal/postmaster/... ./internal/libpq/...` PASS (implementer, 57.4s,
no -count=1); `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`
PASS (tester, ~90s mostly cached); targeted `go test -v -run
'TestSimpleQueryEmptyStringNotNull|...|TestExtendedProtocolRepeatEmptyNotNull'
./internal/postmaster/` PASS (tester, all 4 new tests individually
verified); manual live verification via cgroup-capped server + psql
`\pset null`/`\bind` for both simple-query and extended-protocol paths
(implementer, both blank not NULLMARKER; non-regression `SELECT 'x'; SELECT
'';` still correct); `make ralph-state-guard` — same recurring stale
completed-marker inconsistency as every prior loop, auto-repaired, then
PASS; pre-commit pgbench smoke PASS (347-663-12160 TPS, 0 failed).

Delegation: researcher agent `ab295c97e6fcc0074` (1 round — reproduced the
bug, isolated root cause to all 4 call sites, recommended fix shape);
implementer agent `aa2082fd88aaaf813` (1 round — landed the fix + 4 tests +
manual verification cleanly, no deviations, no 5th call site found); tester
agent `a32ee0c1700278b63` (1 round — confirmed units + postmaster gates
PASS).

In-flight: none. Commit `fd24fa33` pushed to `regress-renumbering`. No
server left running.
