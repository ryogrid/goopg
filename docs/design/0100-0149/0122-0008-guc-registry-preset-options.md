# M0122-0008 — preset parameters and context-specific SET refusals

Status: accepted (2026-10-14). Slice 7 of the task "goopg's GUC registry
diverges from PG 18.3's parameter set". Slices 1–6 are recorded in
[root-0004](../root/root-0004-configuration-and-guc.md).

## Problem

PG 18.3 defines 186 parameters that goopg did not register. Every one of them
failed SHOW, SET and `current_setting()` with "unrecognized configuration
parameter". The first group to fix is the read-only "Preset Options" that
clients query on connect. SQLAlchemy, for example, runs
`SHOW max_identifier_length`.

Separately, a SET that a parameter's context forbids was always answered
"cannot be changed now" with SQLSTATE 22023.

## Change

- These PGC\_INTERNAL parameters (guc\_tables.c) are now registered with
  `FlagDisallowInFile`:

  | Parameter | Value |
  |---|---|
  | `max_identifier_length` | 63 (NAMEDATALEN-1) |
  | `max_function_args` | 100 (FUNC\_MAX\_ARGS) |
  | `max_index_keys` | 32 (INDEX\_MAX\_KEYS) |
  | `segment_size` | 131072 blocks; SHOW `1GB` |
  | `wal_block_size` | 8192 |
  | `debug_assertions` | `off` |
  | `data_checksums` | boot `off` |
  | `huge_pages_status` | boot `unknown` |

- `data_checksums` and `huge_pages_status` describe the running cluster.
  `cmd/goopg`'s `recordClusterStateGUCs` sets them after `initdb.Open`, as
  PG's startup stores them with `SetConfigOption(PGC_S_DYNAMIC_DEFAULT)`:
  - `data_checksums` comes from pg\_control's `data_checksum_version`;
  - `huge_pages_status` is `off`, because goopg never maps huge pages.
- `cantChangeError` (guc.go) reproduces set\_config\_option's refusal (guc.c)
  for SET, the startup packet and `Variable.Set`. Every case uses SQLSTATE
  55P02, with one message per context:

  | Context | Message |
  |---|---|
  | internal | `cannot be changed` |
  | postmaster | `cannot be changed without restarting the server` |
  | sighup | `cannot be changed now` |
  | su\_backend / backend | `cannot be set after connection start` |

## Evidence

- A script of SHOWs and refused SETs gives byte-identical output, SQLSTATEs
  included, on goopg and on the scratch PG 18.3 oracle.
- Tests:
  - `TestPresetOptionsAreReported` and `TestSetRefusalFollowsContext`
    (`internal/utils/misc`);
  - `TestRecordClusterStateGUCs` (`cmd/goopg`), which initdb's clusters with
    and without checksums.

## Not ported (ledgered)

- `data_directory`, `config_file`, `hba_file` and `ident_file` are
  GUC\_SUPERUSER\_ONLY in PG: only roles with `pg_read_all_settings` may read
  them. goopg has no such read gate, so registering them would expose server
  paths to every role.
- `shared_memory_size`, `shared_memory_size_in_huge_pages` and
  `num_os_semaphores` report PG's shared-memory sizing, which goopg has no
  equivalent for.
- goopg advertises FUNC\_MAX\_ARGS and INDEX\_MAX\_KEYS but does not enforce
  them.
- The startup packet still refuses the backend-start contexts, which PG
  accepts there.
- 178 PG parameters remain unregistered. Several have semantics:
  - `transaction_read_only` (see the read-only defect filed alongside);
  - `quote_all_identifiers` (quote\_ident);
  - `transform_null_equals` (the parser);
  - `array_nulls` (array input).

  They must be wired, not registered as inert values.

## Found on the way

goopg does not enforce read-only transactions:

- `BEGIN READ ONLY; INSERT` succeeds;
- `default_transaction_read_only = on` is ignored and commits writes;
- `SHOW transaction_isolation` does not follow the transaction.

This is filed in fix\_plan as an S2 defect awaiting owner placement. It has no parent, because M0122-0008's lineage budget is exhausted.
