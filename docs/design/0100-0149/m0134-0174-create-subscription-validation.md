# M0134-0174 — CREATE SUBSCRIPTION validated nothing

Status: **landed** (2026-08-29). Regress case `subscription.sql` sized live for
the first time and **PARKED** at 526 diff lines / 29 `^+ERROR` (from 552 / 46).

## The defect

`execCreateSubscription` (`internal/executor/operators_ddl.go`) read exactly two
keys out of the `WITH` map — `enabled` and `slot_name` — and dropped every other
name on the floor. `CreateSubscriptionStmt.Conninfo` went straight into the
catalog row unread. There was no validation stage of any kind. So all of

```sql
CREATE SUBSCRIPTION s CONNECTION 'foo'        PUBLICATION p;
CREATE SUBSCRIPTION s CONNECTION 'i_dont=x'   PUBLICATION p;
CREATE SUBSCRIPTION s CONNECTION 'dbname=d'   PUBLICATION p
    WITH (connect = false, enabled = true);
CREATE SUBSCRIPTION s CONNECTION 'dbname=d'   PUBLICATION p
    WITH (not_an_option = 3);
CREATE SUBSCRIPTION s CONNECTION 'dbname=d'   PUBLICATION foo, testpub, foo;
```

**succeeded**, where PG 18.3 raises, respectively, `invalid connection string
syntax: missing "=" after "foo" in connection info string`, `invalid connection
option "i_dont"`, `connect = false and enabled = true are mutually exclusive
options`, `unrecognized subscription parameter: "not_an_option"` and
`publication name "foo" used more than once`.

This is a correctness gap independent of the regress case: a typo'd connection
string or a misspelt option produced a subscription that *looked* created and
could never replicate, with nothing anywhere to say so. It is the third
instance of a shape this milestone has now named twice —
**"the surface accepts a name nobody looks for"** — after M0134-0160
(reloptions) and M0134-0165a (GUC enum aliases).

Two further behavioural bugs fell out of the same absence:

* `WITH (connect = false)` created an **enabled** subscription. Upstream's
  `connect = false` post-pass overrides the defaults of `enabled`,
  `create_slot` and `copy_data` to false (`subscriptioncmds.c:390-392`);
  goopg defaulted `enabled` to `true` unconditionally.
* An unspecified `slot_name` left the slot name **empty** rather than
  defaulting to the subscription name (`subscriptioncmds.c:632`).

### The cascade

`subscription.sql` reuses one subscription name across a long run of negative
cases. Because the first silently-accepted statement created the subscription,
**20 of the case's 46 divergences were a spurious `subscription already
exists`** rather than the statement's own error — the same cascade M0134-0160
documented for `reloptions.sql`. Fixing validation collapsed those 20 to 3, and
all 3 survivors are downstream of the permission checks that are still
unimplemented (see Deferred).

## What landed

`internal/executor/subscription_options.go` — new file, three upstream ports:

| goopg | upstream |
|---|---|
| `parseSubscriptionOptions` | `parse_subscription_options`, `postgres/src/backend/commands/subscriptioncmds.c:124` (CreateSubscription's `supported_opts`, `:560-567`) |
| `defGetSubscriptionBoolean` / `defGetStreamingMode` / `validateReplicationSlotName` | `defGetBoolean` (`define.c:94`), `defGetStreamingMode` (`subscriptioncmds.c`), `ReplicationSlotValidateNameInternal` (`slot.c`) |
| `checkDuplicatesInPublist` | `check_duplicates_in_publist`, `subscriptioncmds.c:2362` |
| `checkConninfoSyntax` | `libpqrcv_check_conninfo`'s syntax half — `PQconninfoParse` → `conninfo_parse` (`fe-connect.c:6290`) plus the `invalid connection string syntax: %s` wrapper |

### Order is load-bearing

`execCreateSubscription` now reproduces upstream `CreateSubscription`'s own
sequence verbatim, because a statement that is wrong in two ways must report
the same one PG reports, and because **any check placed after the registry
insert would leave the silently created subscription behind** — which is the
cascade itself:

1. `parseSubscriptionOptions` (all `WITH` errors, `:560`)
2. duplicate-name → `subscription "%s" already exists` (`:623`)
3. `slot_name` default = subscription name (`:632`)
4. `checkConninfoSyntax` (`:645`)
5. `checkDuplicatesInPublist` via `publicationListToArray` (`:683`)

Step 2 also fixes the message: goopg surfaced the bare registry sentinel
`catalog.ErrSubscriptionExists` ("subscription already exists"), unquoted and
without the name.

### `specified` is not bookkeeping

Upstream's `specified_opts` bitmask — mirrored here as `subOpts.specified` — is
what makes the *same* clash produce two different messages. With
`slot_name = NONE` and an enabled subscription, PG says
`slot_name = NONE and enabled = true are mutually exclusive options` when the
user wrote `enabled = true`, and `subscription with slot_name = NONE must also
set enabled = false` when `enabled` is merely at its default. Both shapes appear
in the case; a naive implementation that only compares final values gets four of
the eight incompatibility messages wrong.

## Deferred (ledger rows, 2026-08-29 M0134-0174)

* **`pg_subscription` is missing `subskiplsn` and the other PG-18 columns** —
  every `\dRs+` fails with `column "subskiplsn" does not exist`; 19 of the 29
  remaining `^+ERROR`s.
* **`ALTER SUBSCRIPTION` is a `CompatNoopStmt`** — every form except
  `OWNER TO` drains to the statement end and silently does nothing
  (`internal/parser/ddl.go:8660`), so `SET (...)`, `SKIP (...)`,
  `CONNECTION`, `ENABLE`/`DISABLE`, `REFRESH PUBLICATION` and
  `ADD`/`DROP PUBLICATION` neither act nor validate. This is why the new
  validators are **not** shared with an ALTER path: there is none. It is also
  the reason the classic sibling-path rule does not apply to this change.
* **No permission or transaction-block checks** — `pg_create_subscription`
  membership, the database `CREATE` ACL, `password_required=false` being
  superuser-only, `must_use_password`, and
  `PreventInTransactionBlock` for `WITH (create_slot)`. The 3 surviving
  "already exists" errors are all downstream of these.
* **`WITH (create_slot)`** — a bare boolean option with no `= value` is a
  syntax error in `parsePubSubWithList`; PG's `def_arg` reads it as true.
* **The `WITH` clause is a map** — same defect M0134-0160a filed for
  reloptions: duplicate options cannot be detected
  (`errorConflictingDefElem`), and with two bad names goopg reports the
  lexicographically-first where PG reports the source-order-first.
  `binary = 0` and `binary = '0'` are also indistinguishable, where upstream
  accepts only the integer.
* **`synchronous_commit` is accepted unvalidated** — upstream runs the value
  through `set_config_option` in `PGC_S_TEST` mode; the GUC registry is not
  reachable from the DDL operator.
* **Validated but not stored** — `binary`, `streaming`, `two_phase`,
  `disable_on_error`, `password_required`, `run_as_owner`, `failover` and
  `origin` are now checked and then discarded; `catalog.Subscription` has no
  fields for them. Same shape as M0134-0160b.
* **URI conninfo is accepted unvalidated** — `postgres://` / `postgresql://`
  dispatch to `conninfo_uri_parse` upstream; only the keyword=value form is
  ported.
* **`ReplicationSlotValidateName` is not shared with the slot registry** —
  the walsender `CREATE_REPLICATION_SLOT` path and
  `pg_create_*_replication_slot` do not run it.

## Verification

* Guard `internal/executor/subscription_options_test.go` — every upstream
  message and SQLSTATE pinned (note the errcodes are *not* uniform: `42601`
  for the option and conninfo errors, `22023` for `origin`, `42602`/`42622`
  for slot names, `42710` for the duplicate name and publication), plus the
  end-to-end cascade guard asserting nothing reaches the registry when
  validation fails. **Revert-checked at both wiring points**: removing the
  `checkConninfoSyntax` call or the `parseSubscriptionOptions` call fails it.
* 8-case regress A/B vs a HEAD worktree (`subscription`, `object_address`,
  `publication`, `psql`, `dependency`, `misc_functions`, `alter_generic`,
  `event_trigger`): **7 byte-identical**, `subscription` 552 → 526.
  `alter_generic` moved 843 → 841 on one run and was proven nondeterministic
  by an A/A on the unchanged patched tree (843, byte-identical to baseline) —
  the flapping line is `catalog update: freshly extended page did not accept
  tuple`, unrelated to this change and pre-existing at HEAD.
