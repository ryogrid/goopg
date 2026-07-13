# 01 — Single-stream WAL: the `EmitCanonical` switch

> **Implementation note (S4, 2026-07-13)**: the switch landed env-only
> (`GOOPG_WAL_CANONICAL` resolved by `emitCanonicalDefault()` at every
> `initdb.Open()`); the §3.1 `OpenOptions.EmitCanonical` field and
> `testutil.WithCanonical` helper were not needed — in-process suites flip
> modes with `t.Setenv`, subprocess harnesses inherit the env.

status: design · date: 2026-07-13 · base: `e453e3f2` · slices: S1 (switch,
default ON), S4 (default flip) · gates: see [README](README.md)

## 1. Problem and numbers

goopg writes two record families into one physical WAL stream. Per pgbench
`-N` transaction (`../perf-optimize3/01-results.md`):

| component | bytes |
|---|---|
| canonical `XLOG_HEAP_INPLACE` (accounts HOT update), unconditional 8 KB image | ~8,240 |
| canonical `XLOG_HEAP_INSERT` (history), unconditional 8 KB image | ~8,240 |
| native first-touch FPIs (~2 per txn at the bench's ~1.4 touches/page) | ~16,400 |
| native logical records (HotUpdate 141 + Insert 85 + Commit 5) | ~231 |
| **total** | **≈ measured 33,004** (component estimates) |

The canonical family exists solely so a **real PostgreSQL 18 standby** can
replay goopg WAL and `pg_waldump` can decode rmgr content. That compatibility
is now deferred (README non-goals) — so the canonical rows above simply stop
being written.

## 2. Current wiring (verified at `e453e3f2`)

Canonical emission is already centralized behind **three choke points**, all
in `internal/initdb/open.go`, all gated on `walWriter.PageHeadersEnabled()`:

1. **`open.go:2060-2066`** — builds the `logCanonical` closure and hands it to
   `executor.Context.LogCanonical`. When nil, every emitter no-ops on its
   existing `ctx.LogCanonical != nil` / `logFn == nil` guards:
   `emitCanonicalHeapInsert/HotUpdate/Delete/PruneLocked`
   (operators_storage.go ~:8240-8339 at `e453e3f2`; this file's citations
   shift +13-14 lines in a tree carrying the current unrelated lockrows WIP),
   the VACUUM prune
   (`VacuumOptions.LogCanonical`, operators_vacuum.go:77), the catalog-heap
   writer (`writeHeapRowCanonical`, operators_ddl.go:13453), the sys-catalog
   btree paths (`sys_catalog_index_insert.go:300`,
   `sys_catalog_btree_split.go:416-440`,
   `sys_catalog_btree_multilevel.go:371/:449`), and the datfrozenxid in-place
   writer (operators_vacuum_datfrozenxid.go:132).
2. **`open.go:943-960`** — the xact-marker logger appends inline canonical
   `XLOG_XACT_COMMIT`/`XLOG_XACT_ABORT` records
   (`BuildCanonicalXactCommit/AbortPayload`) right after the native
   `EncodeXactCommit`/`EncodeXactCommitInval`/`EncodeXactAbort` appends
   (open.go:904-929) — two `Append` calls per commit today.
3. **`open.go:2073`** — `wal.ReportParameters` → `XLOG_PARAMETER_CHANGE`
   (parameter_change.go), a one-shot ~50 B startup record.

**Legacy-mode precedent — scoped precisely**: the `internal/wal`
legacy-format tests (`PageHeaders=false` in their `wal.Config` literals) run
with `LogCanonical=nil` and recover — proof at the WAL layer that native
replay is self-sufficient. **However `initdb.Open` hardcodes
`PageHeaders: true` (open.go:397; `OpenOptions` has no PageHeaders knob), so
the full-server initdb recovery suite runs WITH canonical today** — it will
exercise native-only for the first time when `EmitCanonical` defaults off.
That is exactly why S3's native-only crash proof is mandatory rather than
redundant.

## 3. Design

### 3.1 The switch

A new boolean, **not a GUC**:

- `wal.Config` / `initdb.OpenOptions` field `EmitCanonical bool`
  (default **false** after S4; S1 introduces it defaulting **true**);
- env override `GOOPG_WAL_CANONICAL={on,off}` — read at
  **`Open()`/config-build time via a package-level default**, NOT only in
  `cmd/goopg`: the 29 `internal/initdb/*_recovery_test.go` suites call
  `Open(OpenOptions{...})` directly and `internal/wal` tests build
  `wal.Config` literals, so a cmd-only env read would leave every in-process
  suite unswitchable (the both-modes G-crash/G-unit gates and the nightly
  canonical-on lane would be unrunnable). `cmd/goopg` may additionally set
  the field explicitly. Provide a `testutil` helper (`WithCanonical(bool)`)
  for suites that pin a mode. (The `GOOPG_LOG_STATEMENT` precedent proves
  env-reading is idiomatic but lands on `server.Config` — the plumbing
  target here is the `OpenOptions`→`wal.Config` path.)
- accessor `walWriter.CanonicalEnabled()` alongside `PageHeadersEnabled()` —
  the single source of truth for choke points 1-2 and the BASE_BACKUP guard.

Not a GUC because there is no PostgreSQL equivalent to map a `BootVal` to,
and the repo's GUC discipline (0108-0001: every registered GUC needs a
PG-faithful BootVal + postgresql.conf.sample entry + sync test) would force a
goopg-private entry into the PG-parity sample template.

### 3.2 Gating

The three choke points change from `if walWriter.PageHeadersEnabled()` to
`if walWriter.PageHeadersEnabled() && walWriter.CanonicalEnabled()` — for
points (1) and (2) only. Because every downstream emitter already no-ops on
the nil closure, gating the closure construction covers all DML/DDL/vacuum
emitters with no per-site edits.

### 3.3 Retained records (NOT gated)

| record | why it stays |
|---|---|
| **checkpoint record** (`EncodeCheckpointCompat`, 88-byte PG `CheckPoint` struct, checkpointer.go:552) | not canonical-enveloped at all — appended directly and classified `RmgrXLog + XLOG_CHECKPOINT_SHUTDOWN` (0x00 — format.go:243, deliberate per the M0105-0009 note there, not ONLINE); goopg recovery shape-matches it (`isCheckpointRecord`, recovery.go:10276: `len==1 || len==88`). Untouched by this design. |
| **`XLOG_PARAMETER_CHANGE`** (choke point 3) | goopg's own recovery replays it (`replayXLogParameterChange`, recovery.go:9244→9342) to sync a goopg standby's pg_control GUC echoes. ~50 B, once per startup — no perf relevance, and gating it would silently drift goopg-standby pg_control. **Choke point 3 keeps only the `PageHeadersEnabled()` gate.** |

### 3.4 PageHeaders stays ON (format/content decoupling)

`PageHeaders` controls the **frame format** — PG-compatible page headers,
segment naming, `xl_prev` chains, CRCs — which native-only still wants:
native records are wrapped as valid PG `XLogRecord`s with
`Rmid=RmgrXLog, info=0xF0` (`classifyXLogRecord`, format.go:217-276), so the
stream remains **structurally valid PG WAL** that `pg_waldump` parses without
error (W-001 keeps passing) and that a future re-enable doesn't have to
migrate. This design changes **content only**; `PageHeaders` and
`EmitCanonical` become explicitly independent knobs (today canonical piggybacks
on PageHeaders — the one coupling this design severs).

### 3.5 Mixed-family WAL directories

A cluster that ran with canonical ON and flips OFF (or vice versa via the env)
has both families in `pg_wal/`. Supported by construction: goopg recovery
replays native records via `ApplyRecord`'s native switch and canonical records
via `replayDecodedXLogRecord` (idempotent page-LSN interlock), and always has.
S3 adds a cheap restart test across a flip to pin it.

### 3.6 Decision log

| # | decision | rationale |
|---|---|---|
| D1 | Gate, don't delete: canonical builders/emitters/tests stay in-tree | the ledger resume path is "flip the switch + land C1"; the canonical machinery unit tests (which inject their own `LogCanonical`) remain the regression guard for it |
| D2 | Switch is a Config/env knob, not a GUC | no PG equivalent; sample-template discipline (0108-0001) |
| D3 | PageHeaders unchanged (frame stays PG-valid) | zero data-dir migration; W-001 tripwire stays meaningful; re-enable is content-only |
| D4 | `XLOG_PARAMETER_CHANGE` retained | goopg-standby pg_control sync consumer exists in goopg's own recovery; negligible size |
| D5 | The xact-marker canonical append (choke 2) is gated as a unit with choke 1 | a native-only stream with canonical commit records (or vice versa) is a nonsensical intermediate; one switch, one meaning. Note: today choke 2 also bumps the commit's flush-wait `endLSN` to the canonical record's end (open.go:944-960); gating it cleanly leaves `endLSN` at the native record — no test asserts the canonical bump (verified) |

## 4. What a `-N` transaction's WAL becomes

| record | today | native-only |
|---|---|---|
| native FPI (first touch per epoch, per page) | yes | yes (unchanged; doc 03 fixes its reset ordering) |
| native `RecordKindHeapHotUpdate` | 141 B | 141 B |
| native `RecordKindHeapInsert` (history) | 85 B | 85 B |
| native `RecordKindXactCommit` | 5 B | 5 B |
| canonical `XLOG_HEAP_INPLACE` + 8 KB image | ~8,240 B | — |
| canonical `XLOG_HEAP_INSERT` + 8 KB image | ~8,240 B | — |
| canonical `XLOG_XACT_COMMIT` | ~50 B | — |

Volume math and convergence: doc 05.

## 5. Open questions (flagged)

- **O-01-1**: exact plumbing home for `EmitCanonical` (OpenOptions → wal.Config
  vs a Runtime field) — implementer's choice; keep it readable from
  `walWriter` so choke points 1-2 and the BASE_BACKUP guard (doc 04 §3) share
  one source of truth.
- **O-01-2**: should `GOOPG_WAL_CANONICAL=on` be wired into the nightly matrix
  so the canonical path keeps compiling *and running* until the resume?
  (Recommended: one nightly lane; doc 04 §5.)
