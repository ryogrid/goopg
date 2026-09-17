Task: M-NIGHTLY `limit — FETCH BACKWARD sign/row bug` (banner: P0-E6 still
`[!]`, item 3-6 recon-only tasks exhaustively re-scanned and all remain
blocked on `:65433`/owner-decision — see below — so per the banner's "While
P0-E6 waits" ordering, dropped through to M-NIGHTLY). DONE + committed
(pending this loop's commit).

Files: `internal/postmaster/dispatch.go` (`executeFetch`'s `FETCH BACKWARD`
branch rewritten), `.ralph/fix_plan.md` (task checked off with full root-cause
note; parent `testport/TestPort_RegressSuite` task annotated).

Key symbols: `executeFetch` (`internal/postmaster/dispatch.go:4562`), the
former two-branch backward code (`fetchAll`/finite, now unified into one
loop), `cur.Pos`/`cur.AtEnd` (`cursorEntry`, mirrors PG's
`tuplestore.c` `current`/`eof_reached`).

Findings: M0134-0056's `FETCH BACKWARD` formula (`end := cur.Pos - 1`,
unconditionally excluding the row at `cur.Pos-1`) was wrong — ported real
PG's `tuplestore_gettuple` backward algorithm
(`postgres/src/backend/utils/sort/tuplestore.c:985-1005`) and validated it
tuple-by-tuple against a disposable scratch cluster (`initdb`+`psql` on a
throwaway port 5599, `/tmp/pgscratch1` — never the shared/reference
clusters) plus a hand-trace of all 5 cursors in `limit.sql`. The exclude-the-
current-row behavior only applies when the cursor reached its position via a
finite forward fetch that returned rows; when truly `AtEnd` (`FETCH ALL`, or
a forward fetch that ran off the end), backward re-returns the last row
instead of skipping it (real PG's "first call after eof_reached doesn't
charge a decrement"). The old `FETCH BACKWARD ALL` branch had the same
`AtEnd`-blind bug. Fixed by replacing both closed-form branches with one
loop replaying the C algorithm call-by-call. This also explained the
deep-diff `c5`/`WITH TIES` mystery (`fetch all in c5` returning only 1 of 2
rows) — same position bug, NOT a Limit/WITH-TIES executor gap; no executor
change was needed.
Banner re-scan (recorded so the next loop doesn't redo it): M0143 has zero
selectable non-TPC-H tasks (only `M0143-0007b`, blocked on an owner decision
re: reversing bpchar trimmed-storage). Items 3-6's recon-only candidates
(M0141-S2b-9/-8, M0140-0006a — all explicitly gated on TPC-H byte-identical
captures or the banner's "cost diagnosis only" restriction) are still
implementation-only/blocked; `M0142-0005a`/`M0142-0003i` still need
`tpch-spotcheck.sh` (blocked by the `:65433` hold). M0141-S7's own "cost
diagnosis" scope is DONE per its 2026-09-18 update.

Next step: next loop re-reads the banner fresh. P0-E6 still owner-run. If
still `[!]`, re-check `ci/logs/action-items.md` for anything new, then pick
the next open M-NIGHTLY item — `numerology` (binary/octal/hex integer
literals, parser/grammar change, read the goyacc playbook first) is the next
concrete, well-scoped candidate.

Gates run: `go build ./...` clean. `go test -v -run
'^TestPort_RegressSuite$/^limit$' ./internal/testport/` PASS. Full
`TestPort_RegressSuite`: 48 PASS / 1 FAIL (`numerology`, pre-existing,
unrelated) / 183 SKIP (was 47/2/183 before this loop). `go test
./internal/postmaster/...` PASS. `RALPH_PRECOMMIT_SCOPE=units
scripts/ralph-precommit-test.sh` PASS (all in-scope packages; script's
`EXCLUDE` list omits `internal/postmaster`, covered by the explicit run
above). `make ralph-state-guard` PASS (self-repaired a stale
completed-marker from the prior loop's clean exit, unrelated to this loop's
work). No TPC-H/sf025 gate needed — no `internal/executor`/`internal/optimizer`
row-count path touched (pure cursor/portal FETCH bookkeeping in
`internal/postmaster`).

In-flight: none.
