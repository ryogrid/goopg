(idle — nothing in flight)

M0131-S23 LANDED (loop #172) — "the cheap tail": the four rmgrs above RM_SEQ_ID
that a real PG emits and goopg never will now all have arms.

Files: `internal/wal/recovery.go` (four new dispatch arms +
`replayDecodedXLogGeneric` / `applyGenericPageDelta` /
`replayDecodedXLogCommitTs` / `commitTimestampsTracked`),
`internal/wal/pg_xlog_decode.go` (opcode constants),
new `internal/wal/redo_cheap_tail_pg_test.go`,
`internal/wal/reader_fail_closed_test.go` (S16.1's refusal list shrank to
SPGist+BRIN — the four rmgrs it covered must now be ACCEPTED),
design `0131-0015` §S23 + Guard 13 (old 13 → 14), README index,
fix_plan S23 checked, 2 ledger rows.

Worth carrying:
- RM_GENERIC's `memset(page+pd_lower, 0, pd_upper-pd_lower)` is NOT hygiene.
  GenericXLogFinish diffs a page whose hole is already zero and never logs a
  byte inside it, so dropping the memset yields a page that differs from the
  primary's byte for byte — invisible to queries, fatal to a checksum.
- RM_COMMIT_TS is conditional on purpose: neither opcode carries a timestamp
  (they ride xact_redo_commit), so with tracking off there is nothing to be
  inconsistent with, and with it on a silent skip leaves a pg_commit_ts whose
  extent disagrees with the cluster's own belief.
- Both no-op arms keep their `default:` refusal; a recognised rmgr must not
  become a silent sink. The two-halves test shape (defined opcode accepted /
  undefined opcode still refused) is the guard worth copying into S25.

Gates: UNITS precommit PASS (`internal/wal` 8.6s, `internal/initdb` cached),
`internal/wal` full package PASS, `-race` on the touched wal tests PASS,
`TestE2E_GoopgCrashStartOnPGDataDir` + `TestE2E_GoopgColdStartOnPGDataDir` PASS
(`-count=1`, since those E2Es launch via `go run` and the cache is blind to
`internal/wal`), pgbench smoke via the commit hook, `make ralph-state-guard` OK.
Fail-when-broken proven: deleting the hole memset fails the generic guard.

Nightly triage: `ci/logs/action-items.md` still run `20260812-005501`; all 4
`## AI-` items already filed under M-NIGHTLY, nothing new.

Next loop (banner = M-NIGHTLY filing, then M0131): S25 (index-AM boundary —
named refusals for Hash/Gin/Gist/SPGist/BRIN naming the AM and the LSN) is the
cheapest remaining slice; the two S21g ledger rows (new_xmax,
ALL_VISIBLE_CLEARED) are still small and unclaimed. S24 (MultiXact) and S29 are
the larger open ones.

In-flight: none.
