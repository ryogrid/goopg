# M0122-0008: the SSL GUC family, as the oracle's no-SSL build defines it

Status: landed 2026-09-24. Task: `.ralph/fix_plan.md` M0122-0008 (auth /
roles / multi-DB isolation / encoding).

## Why this slice

The `unimplemented_feat.json` entries for M0122-0008's auth group are
SASLprep, channel binding and `scram_iterations`. Re-verified at HEAD:

- SASLprep is a full port of `src/common/saslprep.c`
  (`internal/libpq/auth/saslprep.go`).
- `scram_iterations` is registered and feeds `CREATE/ALTER ROLE … PASSWORD`
  (`role_ddl.go resolveScramIterations`).
- Channel binding (`SCRAM-SHA-256-PLUS`, `tls-server-end-point`) is the
  remaining scope. It needs TLS, and goopg has none: `handleStartup`
  answers every SSLRequest with `'N'`.

That rejection is also what the oracle does. `postgres/local_install` is
configured without `--with-ssl` (`pg_config.h`: `USE_OPENSSL` undefined), so
PG 18.3 as built here:

- answers SSLRequest with `'N'` (goopg already matches);
- still registers the whole `ssl*` GUC family, with the no-SSL boot values;
- rejects `ssl = on` in its check hook (`check_ssl`, `commands/variable.c`):
  "SSL is not supported by this build".

goopg registered none of those GUCs, so `SHOW ssl`, `SELECT
current_setting('ssl')`, pg_settings readers and any `postgresql.conf` that
names an SSL setting failed with "unrecognized configuration parameter".
This slice registers the family exactly as the oracle build defines it. A
TLS server, and the channel binding that depends on it, cannot be checked
against this oracle and are an owner decision (see the last section).

## What PG defines (guc_tables.c, no-SSL build), measured on PG 18.3

| name | type | context | boot value | notes |
|---|---|---|---|---|
| ssl | bool | sighup | off | `check_ssl` rejects on |
| ssl_ca_file, ssl_crl_file, ssl_crl_dir, ssl_dh_params_file, ssl_passphrase_command, ssl_tls13_ciphers | string | sighup | '' | the last three are GUC\_SUPERUSER\_ONLY |
| ssl_cert_file | string | sighup | server.crt | |
| ssl_key_file | string | sighup | server.key | |
| ssl_ciphers | string | sighup | none | OpenSSL builds: HIGH:MEDIUM:+3DES:!aNULL |
| ssl_groups | string | sighup | none | OpenSSL builds: X25519:prime256v1 |
| ssl_prefer_server_ciphers | bool | sighup | on | |
| ssl_passphrase_command_supports_reload | bool | sighup | off | |
| ssl_min_protocol_version | enum | sighup | TLSv1.2 | TLSv1, TLSv1.1, TLSv1.2, TLSv1.3 |
| ssl_max_protocol_version | enum | sighup | '' | '', TLSv1 … TLSv1.3 |
| ssl_library | string | internal | '' | "Preset Options"; OpenSSL builds: OpenSSL |
| ssl_renegotiation_limit | int | user | 0 | range 0..0; NO\_SHOW\_ALL, NOT\_IN\_SAMPLE, DISALLOW\_IN\_FILE |

Measured behaviour on the oracle (throwaway cluster, port 5534):

- `SET ssl = on` → "parameter "ssl" cannot be changed now" (SIGHUP context).
- `ALTER SYSTEM SET ssl = on` → ERROR "SSL is not supported by this build".
- `ssl = on` in postgresql.conf at startup → LOG "SSL is not supported by
  this build", then FATAL "configuration file … contains errors".
- the same line on reload → LOG plus "contains errors; unaffected changes
  were applied"; `ssl` stays off.
- `SET ssl_renegotiation_limit = 5` → "5 is outside the valid range for
  parameter "ssl_renegotiation_limit" (0 .. 0)"; `= 0` succeeds.
- `SHOW ALL` lists 16 ssl rows; `ssl_renegotiation_limit` is hidden.

## What goopg does now

- `internal/utils/misc/defaults.go` registers all 17 with the values above.
  - `ssl`'s CheckFn returns a `ValidationError` "SSL is not supported by
    this build" for on, so every path that validates a value (ALTER SYSTEM,
    the config-file load, SET after the context check) reports PG's text.
  - `ssl_renegotiation_limit` needs a CheckFn for its range: goopg's int
    canonicaliser reads MinVal = MaxVal = 0 as "no range", so 0..0 cannot be
    expressed by bounds.
- **New `FlagNoShowAll`** (guc.h GUC\_NO\_SHOW\_ALL). `SessionRegistry.AllDisplay`,
  which feeds every SHOW ALL path (simple, extended, executor), skips flagged
  variables. Set on `ssl_renegotiation_limit` and on the three GUCs goopg
  already registered that PG flags the same way: `is_superuser`,
  `session_authorization`, `default_with_oids`. Those three had been listed
  by goopg's SHOW ALL and not by PG's.
- `postgresql.conf.sample` gains PG's `# - SSL -` block. `ssl_ciphers` and
  `ssl_groups` show `'none'`: goopg's sample test requires each shown value
  to equal the boot value, and PG's static sample carries the OpenSSL values
  even in a no-SSL build.
- `pg_settings` (static rows in `catalog.go`) gains the 16 visible rows,
  text byte-for-byte from the oracle.

## Not done here (ledgered)

- **TLS server + SCRAM-SHA-256-PLUS.** Needs an owner decision. The oracle
  build cannot accept TLS, and neither can its psql/libpq (also built
  without SSL), so a goopg TLS server would diverge from the reference and
  could not be verified against it. Resume point: `handleStartup`'s
  SSLRequest arm, then `scram.go`'s `p=` branch.
- GUC\_SUPERUSER\_ONLY is not modelled: PG hides `ssl_ciphers` & co. from
  non-superusers without pg\_read\_all\_settings, in SHOW, SHOW ALL and
  pg\_settings.
- pg\_settings is a static list; its `setting`/`source` columns never show a
  value set in postgresql.conf.
- FlagDisallowInFile is not enforced by ALTER SYSTEM or the config-file
  loader (PG: "parameter … cannot be changed").
