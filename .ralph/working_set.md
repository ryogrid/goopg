(idle — nothing in flight)

Task just completed: M0134-0091 (async.sql). Sized live against the PG 18.3
oracle (scripts/pg-regress-runner.sh --verbose async). Case was 35-line diff
/ 0% parity from two narrow gaps (LISTEN/NOTIFY itself, M0118-0009, was
already fully correct):
(1) exprType's FuncCall switch (internal/optimizer/planner.go) had no arm
for pg_notification_queue_usage() — it fell through to "unknown", and psql
left-justifies an unknown-typed cell instead of right-justifying numeric, a
silent column-alignment divergence (runtime value "0" was already correct).
Added a float8 arm next to the existing random/random_normal/drandom case.
(2) pg_notify(channel, payload) (internal/executor/expr.go) had zero
channel-name validation: NULL was silently treated as a no-op, empty/
over-length channels were silently accepted. PG's Async_Notify
(postgres/src/backend/commands/async.c:604-621) substitutes a NULL channel
with "" then raises ERRCODE_INVALID_PARAMETER_VALUE (22023) "channel name
cannot be empty" / "channel name too long" (>= NAMEDATALEN=64) — neither
ereport calls errposition(), so Pos must stay 0 (no LINE/^ pointer),
matching the M0134-0070 no-errposition pattern. First attempt set
Pos: x.Pos() and still diverged (added 2 spurious LINE/^ lines) — fixed by
dropping Pos entirely. Landed internal/executor/async_notify_test.go
(TestPgNotificationQueueUsageIsFloat8, TestPgNotifyChannelNameValidation).
Case closed clean (no PARK): PASS async (35 lines), 100% parity. CSV row
flipped not-tried -> pass/pass_required=yes (had to de-comma-and-quote the
rationale text — the CSV has no quoting/escaping for internal commas or
quote chars, confirmed via make check-testport-inventory failing twice on
"bare \" in non-quoted-field" then "wrong number of fields" before the fix).
Ledger row added noting the NOTIFY-statement form (internal/postmaster/
notify.go's notifyStmtTag, case *parser.NotifyStmt) is UNAUDITED for the
same channel-length gap — confirmed by reading the code (zero validation,
just buffers n.Channel), not yet exercised by any regress/isolation case so
no observed divergence. Design doc
docs/design/m0134-0091-async-notify-column-type-and-channel-validation.md.
Committed 5a5392cd and pushed... (verify push landed — see Next step).
fix_plan.md M0134-0091 marked [x].

NEXT LOOP: per the Current Priority banner in .ralph/fix_plan.md, continue
M0134 top-to-bottom — next unworked item is **M0134-0092 (bit.sql, status
`not-tried`)**. First: `git log --oneline -1 origin/regress-renumbering` (or
`git push` if the commit above didn't already push) to confirm 5a5392cd
landed on the remote. Then size bit.sql live against ./postgres oracle via
scripts/pg-regress-runner.sh --verbose bit (background, generous timeout;
clear tmp/regress-goopg-data first if a prior run left it non-empty —
`rm -rf tmp/regress-goopg-data`, the runner errors "Directory not empty" on
a stale leftover from an interrupted run). bit.sql exercises the bit/varbit
types — check whether goopg's bit-string support already exists (search
internal/executor for "bit"/"varbit" codec/operators) before assuming a
large gap. CAUTION carried forward from M0134-0086/0090: watch `ps -o rss`
on the goopg PID while any regress file runs (some cases drove RSS to 20+ GB
in <2 min); kill -KILL promptly (never bare pkill -f) if RSS climbs unbounded
before deciding whether it's worth fixing first. If bit.sql resolves to a
small/contained diff like async.sql did, land the fix + ledger + design doc
+ CSV flip in one loop (M0134's established per-task pattern: PARK on a
multi-root-cause case after landing the one contained fix, CLOSE clean on a
small one).

Gates run: `go build ./...` clean; targeted go test -run
'TestPgNotificationQueueUsageIsFloat8|TestPgNotifyChannelNameValidation'
./internal/executor/ PASS; RALPH_PRECOMMIT_SCOPE=units
ralph-precommit-test.sh PASS (full suite, ~530s dominated by
internal/initdb); make check-testport-inventory PASS (after CSV
comma/quote fix); make regen-testport ran clean; make ralph-state-guard
PASS (self-repaired a stale progress.json completed-marker, same recurring
pattern as prior loops — see the guard's own repair message, not a new
bug); pre-commit hook's pgbench smoke PASS (325/606/11945 TPS across the 3
pgbench transaction types).

In-flight: none.
