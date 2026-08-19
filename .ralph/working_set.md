# Working set — M0134-0009 PARKED (parser gaps); real engine fix LANDED; next is M0134-0010

**Task:** M0134-0009 (`select_views.sql`). Parked like 0008 — but unlike 0008 this
loop extracted a **real, shipped engine fix** from the sizing round.

**What landed.** Session identity: `current_user` / `current_role` / `user` /
`session_user` were the hardcoded literal `"postgres"`
(`internal/executor/expr.go`), so `SET SESSION AUTHORIZATION` was invisible to
every query. Design: `docs/design/m0134-0009-session-user-identity.md` (indexed).
New `Context.SessionUser` + `SetRoleIsActive`, threaded from the site that
already seeds the `session_authorization` GUC; `SET ROLE` split from
`SET SESSION AUTHORIZATION` (they were literally the same closure);
`current_role` added to `IsNoParenFuncName`.

**The lesson worth carrying (put in the design doc's Review section):**
**adding a field to `executor.Context` is a SEVEN-site change.** The brief named
2 siblings (`dispatch.go`, `query.go` fast path); the implementer found 2 more
(`dispatch_extended.go`, `extended.go`); adversarial review found 3 more
(`conn_tx.go` SET LOCAL snapshot/restore, `parallel_worker_ctx.go`, `copy.go`).
Every miss was a **silent wrong answer**, not a compile error — the field stayed
at its zero value and fell back to `"postgres"`. Start any future
Context-state addition from that list.

**Second lesson:** a passing test proves nothing about which path it ran.
`TestDispatchPathSessionUserIdentity` sent SETs over the extended protocol (eaten
by `extended.go`'s fast path) and its SELECT over the simple protocol, so the
dispatch closures it was named for had **zero coverage** — proven by a coverage
profile, not by reading. Reaching them needs the multi-statement simple-query
shape (`SET ROLE alice; SELECT current_user;`).

**Review verdict trail:** reviewer returned DO-NOT-SHIP with 13 findings; round 2
fixed the 7-item ship bar (each FAIL-pre verified by reverting, then restored);
independent tester re-run returned GO.

**Why PARKED.** `select_views.sql` still needs three unrelated gaps: `?#`
operator lexing (`?` is not an operator-start char at all), unary prefix `#`, and
`CREATE SCHEMA <n> CREATE TABLE ...` sub-commands (silently no-op → 52 `ERROR:`
lines loading `create_view.sql`). CSV row stays `failed` → **no
`make regen-testport`**.

**Five deferral rows appended** (2026-08-19, task-id M0134-0009): privilege gates
still read `NonSuperuserRole` while identity reads `EffectiveUserName()`;
`SHOW session_authorization` not tracking `session_user()` (supersedes the
M0134-0008 row); EXPLAIN deparsing `CURRENT_USER` as `current_user()`;
`searchPathSchemas` `$user` still hardcoded; the three `select_views` parser gaps.

**Next step:** select **M0134-0010 (`predicate.sql`)** — a `not-tried` case. Grep
`postgres/src/test/regress/parallel_schedule` for a `depends on` line FIRST, then
`scripts/pg-regress-runner.sh --verbose predicate` to size it.

**Gates run:** `go build ./...` PASS; `go vet` (executor/postmaster/parser) PASS;
`go test ./internal/executor/ ./internal/postmaster/ ./internal/parser/` PASS;
15 named session-identity guard tests PASS; coverage profile confirms
`dispatch.go:363-401` + `dispatch_extended.go:330-371` now non-zero;
`RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh` PASS (postmaster is
excluded from that scope by design — covered explicitly above).

**Delegation:** `tmp/ralph-handoffs/M0134-0009a` (tester, sizing, DONE),
`M0134-0009b` (researcher, DONE), `M0134-0009c` (implementer, 2 rounds, DONE).
**In-flight:** none.
