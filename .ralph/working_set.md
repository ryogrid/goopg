Task: M0005 `parsePrimaryConninfo` sslmode/password follow-up
(`unimplemented_feat.json` task_id `m0005`, "parsePrimaryConninfo does not
parse user, sslmode, password..."). sslmode half is COMPLETE and committed
this loop (`bfbc9fd6`), pushed to origin. password half is genuinely
deferred (see Findings) — this task is CLOSED, not left mid-flight.

Files: cmd/goopg/main.go (`parsePrimaryConninfoFull` gains a 4th return
value `sslmode`, parsed from the `sslmode` conninfo key; both call sites —
`startWalreceiver` and the `parsePrimaryConninfo` wrapper — updated; stale
doc comments fixed); cmd/goopg/main_test.go (`TestParsePrimaryConninfoFull`
extended with an sslmode=require case + default-empty case);
internal/server/walreceiver.go (`WalReceiverConfig.SSLMode` field; new
`checkSSLMode(mode string) error` helper called from `DialWalReceiver`
before the TCP dial); internal/server/walreceiver_test.go (new
`TestCheckSSLMode` table test + `TestDialWalReceiverRejectsUnsupportedSSLMode`
asserting rejection happens before any dial, using an unreachable
127.0.0.1:1 address so a regression would hang/timeout instead of silently
passing); docs/design/0005-0001-streaming-replication-architecture.md (new
"`primary_conninfo` key parsing (2026-07-08)" section) + docs/design/
README.md (0005-0001 row got a "Follow-up (2026-07-08)" sentence);
unimplemented_feat.json (surgical 1-field `code_audit` edit on the m0005
sslmode/password entry, per house rule — status stays "open" since
password is still unhandled; verified valid JSON via json.load after);
.ralph/deferral_ledger.md (new `-` row for the still-open password gap).

Key symbols: `parsePrimaryConninfoFull` (cmd/goopg/main.go); `checkSSLMode`,
`WalReceiverConfig.SSLMode`, `DialWalReceiver` (internal/server/
walreceiver.go); `WalReceiver.handshake` (the reason password can't be
wired yet — no Authentication* challenge handling at all).

Findings: goopg has zero TLS implementation anywhere (`grep -rn "crypto/tls"`
found nothing outside test-name false-positives), so sslmode=require/
verify-ca/verify-full can never be honestly satisfied — the correct v0
behavior is to refuse the connection rather than silently downgrading to
plaintext (a security footgun), which is what this loop implements.
password is a **different kind of gap**: `WalReceiver.handshake` sends the
startup message then loops reading frames until MsgErrorResponse or
MsgReadyForQuery — it never handles any Authentication* message at all
(replication connections are unconditionally trust-authenticated in v0, per
`WalReceiverConfig.User`'s existing doc comment). Parsing a password out of
conninfo with nothing to send it to would be a dead field, so it was left
deferred (ledger row) rather than half-implemented. Confirmed non-vacuous:
saw real compiler errors (WrongAssignCount at all 3 call sites) when the
signature change alone was applied before fixing callers, and the two new
tests (`TestCheckSSLMode`, `TestDialWalReceiverRejectsUnsupportedSSLMode`)
exercise the new rejection path directly.

Next step: pick a fresh item. Good candidates surfaced but not yet
investigated: real password/MD5/SCRAM auth on the replication path (blocked
on server-side walsender auth-checking landing first — see this loop's
ledger row for the coupling); "eager next-segment lookahead for WAL
preallocation" (task_id M0007, background-goroutine scope — read the
`unimplemented_feat.json` entry's code_audit at ~line 496 first);
"restart-the-listener reload for ContextPostmaster GUCs" (fix_plan.md
~line 3246, explicitly noted as matching-upstream out-of-scope, so
probably NOT worth picking); `ColOpClasses`/`ColCollations` real OID
resolution for `indclass`/`indcollation` (fix_plan.md ~line 3195, flagged
"materially larger, separate gap" — good candidate if willing to scope a
full opclass/collation name↔OID registry). Also check whether
min_parallel_table_scan_size/min_parallel_index_scan_size (m0097-0003) has
become actionable — it was "moot until real parallel query execution
lands" as of last check, almost certainly still moot, skip unless parallel
query execution has landed.

Gates run: go build ./... clean. go vet ./... clean (repo-wide). go test
./cmd/goopg/... ./internal/server/... (full packages, includes both new
test functions) PASS. scripts/tpch-spotcheck.sh PASS (Q12=2/Q13=33).
RALPH_PRECOMMIT_SCOPE=smoke scripts/ralph-precommit-test.sh PASS (0
failed, all 3 workloads) — run standalone, then again as the actual git
pre-commit hook on the real commit. make ralph-state-guard: 1 benign issue
auto-repaired (same recurring status/progress clean-exit-vs-in_progress
reconciliation noted every prior loop — not new, do not chase it). Commit
bfbc9fd6 pushed to origin/align-data-structure-with-pg.

In-flight: none. Mid-loop git hazard (resolved, no data lost): a
concurrent peer process appears to be running a nightly-CI-batch loop in
this SAME working tree (unrelated staged files — .gitignore, ci/batch/
run-nightly.sh, ci/logs/launch.log, ci/logs/scheduler.log — briefly
appeared staged mid-loop, and a `git stash pop` I ran to prove
non-vacuousness surfaced someone else's old "explain-buffers-dirtied-
written" stash instead of my own). I aborted the stash-based
non-vacuousness proof entirely rather than risk further pollution — the
unrelated files disappeared from `git status` on their own by the time I
re-checked (the peer must have committed/restashed), and `git status`
before my own `git add`/`git commit` showed exactly my 8 intended files.
Non-vacuousness for this loop's change was instead established via the
compiler-error evidence noted above. **Lesson for future loops: avoid
`git stash push`/`git stash pop` for non-vacuousness proofs on this
branch — there may be a concurrent Ralph process; the existing
`git diff`-before/after or targeted-revert-with-Edit approach is safer.**
Nightly-triage: `ci/logs/action-items.md` mtime (2026-07-07 03:52) still
predates this loop's start — no new triage needed next loop unless that
file's mtime has moved.
