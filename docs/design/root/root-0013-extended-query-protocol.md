# 0013 — Extended Query Protocol (v0)

- **Status:** accepted
- **Date:** 2026-04-28
- **Supersedes:** —

## Context

Simple Query (`Q`) is already implemented, but pgbench and libpq rely on
extended-query messaging for prepared execution flow and pipelined error
handling:

- `Parse`
- `Bind`
- `Describe`
- `Execute`
- `Sync`
- `Close`

Upstream PostgreSQL treats this as a state machine over named/unnamed
statements and portals:

- `postgres/src/backend/tcop/postgres.c` (`exec_parse_message`,
  `exec_bind_message`, `exec_execute_message`, `exec_describe_statement_message`)
- `postgres/src/backend/utils/mmgr/portalmem.c`

The current goopg server has parser/planner/executor packages, but the wire
layer still drives only Simple Query.

## Decision

### Per-connection state

Each backend connection owns an `ExtendedState`:

```go
type ExtendedState struct {
    Statements map[string]*PreparedStatement // "" is unnamed
    Portals    map[string]*Portal            // "" is unnamed
}
```

`PreparedStatement` stores:

- Raw SQL string
- Parsed statement list (v0: exactly 1 statement)
- Planned tree
- Parameter type OIDs (inferred or provided)
- Result schema metadata for `Describe Statement`

`Portal` stores:

- Pointer to prepared statement
- Bound parameters (`[]executor.Datum`)
- Result-format codes (v0: text only, binary rejected)
- Lazily-built operator and execution cursor state

### Message handling contract

1. `Parse`
- Parse SQL into exactly one statement.
- Run analyzer/planner immediately and cache plan in statement entry.
- Reject multi-statement Parse with SQLSTATE `42601`.

2. `Bind`
- Resolve statement by name.
- Decode bind parameters into executor Datums.
- Create/replace portal entry.

3. `Describe`
- Statement: return parameter metadata + row description from planned output.
- Portal: return row description for that portal's statement.

4. `Execute`
- Build operator from plan at first execute for the portal.
- Stream up to max-rows rows (`0` means all rows).
- Return `PortalSuspended` when max-rows boundary is hit.
- Return `CommandComplete` when drained.

5. `Sync`
- Terminates an extended-message batch.
- Emits one `ReadyForQuery` with correct transaction status.
- On prior error in the batch, skip messages until Sync (upstream behavior).

6. `Close`
- Drop statement or portal by name.
- Return `CloseComplete`.

### Scope (v0)

Included:

- Named + unnamed statements/portals
- Text format parameters/results only
- Single statement per Parse
- SELECT / DML / utility shapes already supported by planner/executor

Deferred:

- Binary format codes
- Multiple statements per Parse
- Protocol-level pipeline mode optimizations
- Generic vs custom plan selection
- Refcursor portals and holdable cursors

### Error and SQLSTATE mapping

- Unknown statement/portal name: `26000` (`invalid_sql_statement_name`)
- Bind parameter count mismatch: `08P01` (`protocol_violation`)
- Unsupported format code: `0A000` (`feature_not_supported`)
- Parse/planner/executor errors pass through their existing SQLSTATEs

### Interaction with Simple Query

`Q` path remains unchanged and independent.

Extended path uses the same parser/planner/executor internals; only wire
message choreography and state retention differ.

## Alternatives Considered

- Implement unnamed-only first and skip named entries.
  - Rejected: many clients (and tests) expect named lifecycle semantics.
- Plan at Execute instead of Parse.
  - Rejected: `Describe Statement` requires plan metadata before Bind/Execute.
- Reuse simple-query text parser in Bind/Execute.
  - Rejected: would duplicate planner wiring and diverge behavior.

## Consequences

- Server message loop grows from request/response to batch state machine.
- Connection objects must own statement/portal maps and cleanup hooks.
- Existing executor operators can be reused directly once portal context
  supplies bound parameters.
