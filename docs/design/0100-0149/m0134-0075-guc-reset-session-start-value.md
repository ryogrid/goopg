# M0134-0075 — `RESET` restores the session-start GUC value, not the compiled default

Status: accepted (2026-08-22)
Task: `.ralph/fix_plan.md` M0134-0075 (`timestamp.sql`, regress-sql `failed`)

## SQL surface

`regress/sql/timestamp.sql:90-93`:

```sql
set datestyle to ymd;
...           -- column SELECTs here render in "ymd" style
reset datestyle;
...           -- every subsequent SELECT must render in the session-start style
```

`scripts/pg-regress-runner.sh` exports `PGDATESTYLE="Postgres, MDY"`, which libpq
folds into the startup packet as `datestyle=Postgres, MDY` and the goopg server
applies at session start. After `reset datestyle`, PG renders `Sun Dec 28 … 2014`
(Postgres/MDY); goopg renders `2014-12-28 19:14:47.000000` (ISO) — the **whole
`@@ -696,1382` block** of the timestamp diff is Postgres-vs-ISO pairs, so this one
bug underlies a large fraction of the case's 2493 diff lines.

## goopg ↔ PG 18.3 divergence

`RESET` must restore the value that was in effect at session start (the
env/startup-packet value), not the compiled `BootVal`. goopg restores `BootVal`.

## Root cause

`internal/utils/misc/session.go`:

- `SessionRegistry` layers two per-session maps — `session` (user `SET`) and
  `local` (user `SET LOCAL`) — above the shared global `Variable.Value`
  (`guc.go:120` carries `BootVal` + `Value` + `Source`; no third slot). Get
  precedence is `local → session → v.Value` (`session.go:79`).
- `Set(name, value, isLocal)` (`session.go:124`) writes `session`/`local`.
- **`Reset(name)` (`session.go:217`) is `delete(session)+delete(local)`** — it
  removes the session override entirely, exposing global `v.Value`. Since
  `datestyle`'s `BootVal` is `"ISO, MDY"` (`defaults.go:55`), and the
  `"Postgres, MDY"` from the startup packet lived only in `s.session`, RESET
  deletes it and reveals ISO.
- `server.go:1214-1226` applies every startup-packet key through the **same
  `sess.Set`** a client `SET` uses, so nothing records "this was the session-start
  value".

## PG oracle

`postgres/src/backend/utils/misc/guc.c`:

- RESET restores **`reset_val`, not `boot_val`** (doc at `:3308-3309`, restore at
  `:3727`).
- `reset_val` seeds from `boot_val` in `InitializeGUCOptions` (`:5156+`).
- `reset_val` is updated only when `makeDefault = changeVal && (source <=
  PGC_S_OVERRIDE) && …` (`:3679`) — i.e. by env/file/argv/database/client sources,
  **not** by user `SET` (`PGC_S_SESSION`). The startup packet is applied at
  `postinit.c:1298` with `SetConfigOption(..., PGC_S_CLIENT)` ⇒ `makeDefault=true`
  ⇒ `reset_val="Postgres, MDY"`.
- `PGDATESTYLE`'s env mapping is client-side libpq (`fe-connect.c:420-424`
  `EnvironmentOptions[]`); the server only ever sees a plain `datestyle` key.

## Fix design

Mirror the `reset_val` semantics with a third per-session map, `startup`:

1. **`SessionRegistry` gains `startup map[string]string`** — the per-session
   reset value (PG `reset_val`), distinct from `session` (user `SET`) and the
   global `v.Value` (config-file/compiled default — already the correct
   process-global RESET fallback via `setFromFile`).
2. **New `SetStartup(name, value)`** — `Set`'s body minus the `local` branch:
   writes `session[name] = value` **and** `startup[name] = value`. This is the
   only writer of `startup`.
3. **`Reset(name)`** becomes: `delete(local[name])`; then `if v, ok :=
   startup[name]; ok { session[name] = v } else { delete(session[name]) }`.
   Startup value wins over the global fallback; absent startup falls through to
   global `v.Value` exactly as today.
4. **Swap the 2 startup-application call sites** at `server.go:1214-1226` from
   `sess.Set` to `sess.SetStartup`.

Everything else is untouched by construction — `Set`/`SetInternal`/`Get`/
`SetLocal`/`ResetAll`/`EndTransaction`/dispatch/parser all funnel through the one
`Reset` choke point (`query.go:390/434`, `extended.go:592/613`, `dispatch.go:493-494`),
and `SET x = DEFAULT` already delegates to `Reset` (`session.go:127`). The S15
transaction-rollback journal `txPrior` (`session.go:31-38/336`) is **orthogonal**
(undo map restored only on `EndTransaction(false)`) and is not reused, though its
`map[string]*string` per-key shape is the precedent.

## Sibling paths (must agree)

- `Set` vs `SetStartup` vs `Reset`: user `SET` must NOT touch `startup`; only
  `SetStartup` writes it. Add reciprocal comments at both `Set` and `SetStartup`
  so the pair cannot drift.
- `RESET` / `RESET ALL` / `SET … = DEFAULT` all reach the same `Reset` body.

## Tests

- `internal/utils/misc/guc_test.go` (or `session_tx_rollback_test.go`): a
  FAIL-pre/PASS-post test asserting `SetStartup("datestyle","Postgres, MDY")`
  → `Set("datestyle","ymd")` → `Reset("datestyle")` yields `"Postgres, MDY"`,
  and the no-startup case `Set(...)` → `Reset(...)` falls through to the global
  `v.Value`.
- Re-run `scripts/pg-regress-runner.sh --verbose timestamp`; expect the
  Postgres-vs-ISO pairs after line 93 to collapse (the ISO residue is the
  function-result path, bucket D2 — out of scope here).

## Out of scope (ledgered separately if surfaced)

Bucket D2 (function-result timestamps bypass DateStyle), E (`date_bin`),
E2 (`date_trunc`), F (`date_part` fields), H (`make_timestamp`), I
(`generate_series`), J (`age`), K (`pg_input_is_valid`), A (input literal parser),
B (int64-ns carrier), G (`to_char`), M (IntervalStyle), O (`interval * 2`).
