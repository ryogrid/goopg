(idle — nothing in flight)

M0131-S21 CLOSED and ticked. **The whole PG-18 opcode space of every handled
rmgr is now asserted at once, and asserting it found a defect.**

Landed:
- `internal/wal/opcode_space_coverage_pg_test.go` (new) —
  `TestReplayOpcodeSpaceCoverageForHandledRmgrs`: 16 rmgrs with a dispatch arm,
  **74 opcode probes** cross-checked against every `#define XLOG_*` in
  `postgres/src/include`, + 14 undefined-value controls. Result: **zero
  coverage holes** — S21a/S21b were genuinely complete.
- `internal/wal/recovery.go` — new `unsupportedDecodedXLogOpcode(r)`; all 16
  rmgr `default:` arms route through it.
- `docs/design/0131-0015-pg-wal-opcode-coverage.md` §"S21 closure".

Worth carrying:
- **Only RM_XLOG's `default:` carried `ErrUnsupportedRecord`; the other 13
  returned a bare error.** Contradicts the contract at `format.go:45-56` (an
  unsupported record is durable, categorically unlike a torn tail). It survived
  fourteen slices because nothing observable changed: BOTH `ApplyRecord`
  callers (`replayRecords`, `StreamReplayer.run`) refuse the start on ANY
  error. The class only matters to a caller that discriminates — none exists
  yet. Generalisable: a sentinel-error contract with no discriminating consumer
  drifts silently; enumerate the space to find it.
- Probes are header-only on purpose (DISPATCH test): any error but the default
  arm's own is a PASS. That forces an EXACT message match — `errors.Is(err,
  ErrUnsupportedRecord)` cannot work, because the deliberate refusals (2PC,
  `HEAP2_REWRITE`, commit_ts-tracked) carry the same sentinel by design.
- RM_BTREE's `default:` is NOT a bare refusal — it is S16.3's FPI fallback, so
  its control expects that wording (`has no block references to restore`).
- Excluded on purpose: RM_HASH/GIN/GIST/SPGIST/BRIN (refused wholesale by rmgr,
  S25 — enumerating opcodes there would assert the opposite of S25's decision);
  RM_MULTIXACT (no arm at all, S24's open work).

Gates: `internal/wal` PASS + `-race` PASS, `internal/initdb` PASS (243 s),
UNITS PASS (no FAIL/panic), `go build ./...` + `go vet` clean, pgbench smoke via
the commit hook. Both halves proven fail-when-broken by scripted reverts
(drop the `XLOG_HEAP_INPLACE` arm → 1 FAIL naming it; drop the class → 13
control FAILs).

Nightly triage: `ci/logs/action-items.md` still run `20260812-005501`; all four
`## AI-` items already filed under M-NIGHTLY — nothing new to file.

Next loop (banner = M-NIGHTLY filing, then M0131): remaining unchecked M0131
items are **S24** (MultiXact durable `pg_multixact` SLRU + `multixact_redo` —
LARGE/RISKY, "decide explicitly": the decision itself is a legitimate loop) and
the deferred S9.4a.. information_schema successors. S24 is the only genuinely
unavoidable missing rmgr.

In-flight: none.
