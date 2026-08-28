# M0134-0165 — `client_min_messages` must gate NOTICE/WARNING delivery

**Status:** landed
**Task:** M0134-0165 (`postgres/src/test/regress/sql/security_label.sql`)
**Date:** 2026-08-29

## The case that surfaced it

`security_label.sql` is one of the smallest cases in the regress suite: eight
`SECURITY LABEL` statements, every one of which is expected to fail, wrapped in
a create/drop scaffold. goopg already implemented the interesting half —
`ExecSecLabelStmt`'s "no security label providers have been loaded" /
`security label provider "dummy" is not loaded`, raised *before* the target
object is resolved (which is why `seclabel_tbl3` and `regress_seclabel_user3`,
neither of which exists, still report the provider error rather than
"does not exist"). All eight statements matched byte-for-byte at HEAD.

The entire 16-line diff came from the case's first two lines of scaffolding:

```sql
SET client_min_messages TO 'warning';
DROP ROLE IF EXISTS regress_seclabel_user1;
DROP ROLE IF EXISTS regress_seclabel_user2;
```

goopg emitted

```
NOTICE:  role "regress_seclabel_user1" does not exist, skipping
NOTICE:  role "regress_seclabel_user2" does not exist, skipping
```

where PG emits nothing.

## Root cause — engine-wide, not case-local

`client_min_messages` existed in goopg only as a GUC *declaration*
(`internal/utils/misc/defaults.go`, `TypeEnum`, `BootVal: "notice"`). A
whole-tree grep found exactly one reference: that declaration. **Nothing
consumed it.** Every NOTICE and WARNING goopg produced went to the client
unconditionally, on every connection, for the lifetime of the project. The
`SET` succeeded and then did nothing.

This is not a `security_label` defect — the case merely happens to be small
enough that the missing GUC is the *only* thing wrong with it. Fifteen upstream
regress cases open with the same `SET client_min_messages TO 'warning'`
(`alter_generic`, `alter_table`, `cluster`, `foreign_data`, `foreign_key`,
`object_address`, `privileges`, `publication`, `rowsecurity`, `vacuum`, the
three `collate.*` variants, `maintain_every`, `security_label`), and each was
carrying the resulting spurious notices as diff noise.

## Upstream behaviour

PostgreSQL decides client visibility once, inside `elog.c`, not at the
`ereport` call sites (`postgres/src/backend/utils/error/elog.c`):

```c
static bool
should_output_to_client(int elevel)
{
	if (whereToSendOutput == DestRemote && elevel != LOG_SERVER_ONLY)
	{
		if (ClientAuthInProgress)
			return (elevel >= ERROR);
		else
			return (elevel >= client_min_messages || elevel == INFO);
	}
	return false;
}
```

Two details in that one-line comparison matter:

- **INFO is exempt.** `elog.h` says INFO is "always sent to client regardless
  of `client_min_messages`". A naive `elevel >= min` gets this wrong.
- **ERROR and above are unconditional.** `client_min_messages` caps out at
  `error` (elevel 21) and ERROR/FATAL/PANIC are 21/22/23, so the comparison
  always admits them. That is why the fix gates notices *only*.

Elevels are `postgres/src/include/utils/elog.h`; the GUC's accepted spellings
are `client_message_level_options` in
`postgres/src/backend/utils/misc/guc_tables.c`.

## The fix

The gate goes where PG puts it: at the single emitter, not at the producers.

- **`internal/libpq/messages.go`** — upstream's elevels as named constants, a
  `severityElevel` map from the non-localized `'V'` severity field back to an
  elevel, a `clientMinMessagesElevel` map for the GUC spellings, and
  `ShouldOutputToClient(severity, clientMin)` reproducing the comparison above
  including the INFO carve-out. `WriteNoticeResponse` consults it and returns
  `nil` (message dropped) when the severity is suppressed.
- **`internal/libpq/frame.go`** — `FrameWriter.ClientMinMessagesFn func() string`,
  following the existing `TxStatusFn` hook precedent for per-connection state
  that the wire layer needs but cannot reach on its own.
- **`internal/postmaster/server.go`** — `runPostStartupLoop` installs the hook
  next to `w.TxStatusFn`, reading the GUC through the session registry.

### Why a choke point rather than 11 call sites

goopg has eleven `WriteNoticeResponse` callers spread over five files
(`dispatch.go` ×5 — including the inline `NoticeFlush` used by the isolation
runner, `extended.go` ×2, `database_ddl.go`, `copy.go`). Filtering at each one
would be eleven chances to forget, and the twelfth producer added next month
would silently bypass the GUC. Routing every producer through one function that
consults the GUC is exactly the structural reason upstream put
`should_output_to_client` inside `elog.c`. A grep for `MsgNoticeResponse`
confirms the single emission site; the other hits are reader-side
(`internal/replication/tablesync.go` consuming notices from an upstream server).

### Why the GUC is read live, not snapshotted

The hook calls `sess.Get("client_min_messages")` on every message rather than
capturing a value at connection start, so `SET`, `SET LOCAL` and `RESET` all
take effect on the next message — matching upstream, which reads the live GUC
variable inside `should_output_to_client`.

### Fail-open, deliberately

An unrecognized severity, an unrecognized GUC value, or a nil hook all send the
message. The failure mode of a wrong *suppression* (a silently swallowed
warning) is far worse than a wrong *delivery*. The nil-hook case also
reproduces upstream's `ClientAuthInProgress` carve-out for free: the hook is
installed after the startup handshake, so anything emitted before it is
unfiltered.

## Deliberately not changed

goopg's `EnumOptions` for `client_min_messages` lists nine values; upstream's
`client_message_level_options` has eleven, the extra two being `debug` (alias
for `debug2`) and `info`, both marked `hidden`. Upstream *accepts* them as
input but omits them from `pg_settings.enumvals`. goopg's `Variable` has no
"hidden option" concept, so adding them to `EnumOptions` would fix the input
rejection while introducing a new `enumvals` divergence. `clientMinMessagesElevel`
already understands both spellings, so only the GUC's input validation is
short; ledgered as its own row rather than half-fixed here.

## Verification

16-case regress A/B against a HEAD worktree (the 11 available cases that set
`client_min_messages`, plus `drop_if_exists`, `transactions`, `plpgsql`,
`create_table`, `temp` as notice-heavy controls):

| case | HEAD | this change | |
|---|---|---|---|
| `security_label` | 16 | **0** | **PASS, byte-identical** |
| `privileges` | 3928 | 3900 | −28 |
| `rowsecurity` | 6243 | 6217 | −26 |
| `foreign_data` | 2518 | 2501 | −17 |
| `alter_generic` | 857 | 841 | −16 |
| `alter_table` | 3766 | 3754 | −12 |
| `object_address` | 598 | 586 | −12 |
| `cluster`, `foreign_key`, `publication`, `vacuum`, `drop_if_exists`, `transactions`, `create_table`, `temp` | — | — | byte-identical |

**Net −127 diff lines, one case newly green, zero regressions.**

`plpgsql` reported 4399 → 4400 and needed adjudication. It is **pre-existing
nondeterminism, not a regression**: the whole delta is one statement
(`select * from f1(42)`, plpgsql.sql:1694) that alternates between
`ERROR: missing expression`, `ERROR: function f1 is not unique`, and returning
two rows. An A/A on the *unchanged* working tree produced 4402 then 4401 with
that same statement flipping — the same build disagrees with itself, so the
A/B delta carries no signal. `plpgsql.sql` never sets `client_min_messages`.
Ledgered separately.

Guard: `internal/libpq/client_min_messages_filter_test.go` — a 20-row table
against upstream's comparison (default GUC, the `warning` setting, `error`,
debug levels, the INFO carve-out, LOG, ERROR/FATAL/PANIC, the hidden aliases,
case-insensitivity, and all three fail-open paths), plus writer-level tests
that the filter lives inside `WriteNoticeResponse`, that a mid-session GUC
change takes effect without rebuilding the writer, that `WriteErrorResponse`
carries no gate, and that severity is keyed on the non-localized `'V'` field
(keying on `'S'` would make suppression locale-dependent). Revert-checked: with
the gate removed the writer-level test fails on the NOTICE that reaches the
wire.

Also run: `RALPH_PRECOMMIT_SCOPE=units scripts/ralph-precommit-test.sh`, and
the mandatory pre-commit pgbench smoke.

## Follow-ups filed

- **M0134-0165a** — `client_min_messages` rejects upstream's hidden `info` /
  `debug` aliases (needs a hidden-option concept on `misc.Variable` so
  `pg_settings.enumvals` keeps hiding them).
- **M0134-0165b** — `plpgsql.sql` is nondeterministic at
  `select * from f1(42)`; a stale `f1` overload survives an earlier
  `drop function`, so the same build reports three different outcomes across
  runs. Blocks byte-level A/B on that case.
