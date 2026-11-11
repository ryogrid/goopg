# M0148\-0001j3 — type USAGE privilege checks, ROLLBACK of role changes, DROP TYPE of a domain

**Parent:** M0148\-0001j2 · **Status:** landed 2026\-11\-11; closed (residue in
M0148\-0001j4)

Code landed in two commits:

- `88ef78511`, committed together with the deepwiki documentation, holds
  items (a) to (d) below.
- The `ddl(M0148-0001j3)` commit of 2026\-11\-11 holds item (e) and the
  gates for the whole change.

## Divergences closed

### (a) Type USAGE privilege, `aclcheck_error_type`

PG 18.3 calls `object_aclcheck(TypeRelationId, typid, GetUserId(),
ACL_USAGE)` wherever DDL makes an object depend on a type. On failure it
raises 42501 `permission denied for type %s`, with no error position. goopg
had no such check, so regress privileges created objects that PG refuses.
Those objects then blocked `DROP DOMAIN priv_testdomain1`.

The check now runs at these sites (`internal/executor/type_usage_acl.go`
`checkTypeUsage`):

| PG site | goopg statement |
|---|---|
| `BuildDescForRelation` (tablecmds.c) | CREATE TABLE columns, CREATE TABLE AS result columns, CREATE TYPE … AS attributes |
| `check_of_type` | CREATE TABLE … OF |
| `ATExecAddColumn` | ALTER TABLE ADD COLUMN, ALTER TYPE ADD ATTRIBUTE |
| `ATPrepAlterColumnType` | ALTER TABLE ALTER COLUMN TYPE, ALTER TYPE ALTER ATTRIBUTE TYPE |
| `DefineDomain` | CREATE DOMAIN base type |
| `interpret_function_parameter_list`, `compute_return_type` | CREATE FUNCTION arguments, then the result |
| `DefineAggregate` | CREATE AGGREGATE argument type |
| `CreateCast` | source, then target |
| `DefineOperator` | left, then right operand |

Rules ported from `pg_type_aclmask` (aclchk.c):

- An array type's ACL is its element type's (`typelem`). `_t` and `t[]`
  both resolve to `t`.
- A NULL `typacl` is `acldefault('T', owner)`, which grants USAGE to the
  owner and to PUBLIC. So only a type that a REVOKE has touched can refuse
  anyone.
- A superuser and the owner always pass.

### (b) GRANT / REVOKE on types: `ExecGrant_Type` and `restrict_and_check_grant`

- `GRANT … ON TYPE _t` raises 0LP01 `cannot set privileges of array types`,
  with HINT `Set the privileges of the element type instead.`
- `GRANT … ON DOMAIN t`, where t is not a domain, raises 42809
  `"t" is not a domain`.
- A grantor that is neither the owner nor a superuser needs the grant
  option:
  - A grantor that holds no privilege at all gets 42501.
  - A grantor that holds the privilege without the option gets a WARNING
    (`no privileges could be granted/revoked for "t"`) and no change.
  - `catalog.InMemory.HoldsGrantOption` reads the stored grant\-option flag.
- After `REVOKE … FROM PUBLIC` has materialized `typacl`, a later GRANT no
  longer brings the PUBLIC entry back.

### (c) ROLLBACK reverts session\-level role changes (AtEOXact\_GUC)

In PG, `AtEOXact_GUC(isCommit=false)` reverts every setting made inside the
aborted transaction, including session\-level `SET ROLE` and
`SET SESSION AUTHORIZATION`. goopg reverted only `SET LOCAL`, so in regress
create\_operator `BEGIN; SET ROLE r; …; ROLLBACK;` left r active for the
next block.

- `connTxState.txRolePrior` snapshots the role triple
  (`NonSuperuserRole`, `SessionUser`, `SetRoleIsActive`) before the first
  role change of the transaction.
- `RestoreTxRoleForRollback` restores it on ROLLBACK paths. This snapshot
  wins over a later SET LOCAL snapshot.
- COMMIT discards the snapshot.

### (d) Smaller CREATE CAST / DROP OPERATOR / DROP AGGREGATE fixes

- **CREATE CAST over a domain.**
  - The cast function's argument and result may match the domain's base
    type. This is `IsBinaryCoercible` with `getBaseType`.
  - goopg now emits PG's WARNING
    `cast will be ignored because the source|target data type is a domain`.
- **User types as arguments.** `DROP AGGREGATE` / `DROP OPERATOR` accept
  user enum, domain and composite types as argument types (`isUserTypeName`).
  Before, only built\-in names were known.
- **`DROP OPERATOR op (NONE, t)`** finds a prefix operator. CREATE OPERATOR
  registers it with an empty LEFTARG.

### (e) DROP TYPE of a domain

`RemoveObjects` resolves `OBJECT_TYPE` through `get_object_address_type`,
which looks the name up in pg\_type with no `typtype` check. Only
`OBJECT_DOMAIN` insists on `TYPTYPE_DOMAIN`. So `DROP TYPE vc4` drops domain
vc4 exactly as `DROP DOMAIN vc4` does.

`execDropType` now resolves a domain first and runs the same
`dropTypeDependents` + `dropDomainObject` path as `execDropDomain`:

- RESTRICT refuses with 2BP01 while a column uses the domain.
- CASCADE drops the column, never the table.

Regress uses it in domain.sql (`drop type vc4;`) and in jsonb.sql's mixed
`drop type jsb_ia, …, jsb_i_not_null_rec`.

## Tests

- `TestTypeUsagePrivilege` (executor) covers (a) and (b): REVOKE FROM PUBLIC
  followed by a GRANT, array\-type and ON DOMAIN errors, refusals for an
  ungranted role at seven sites, passes for a granted role, and the WARNING
  for a grantor that lacks the grant option.
- `TestACLGrantor*` (executor) was updated: a non\-owner grantor needs WITH
  GRANT OPTION first.
- `TestConnTxStateRollbackRevertsSessionRole` (postmaster) covers (c).
- `TestDropTypeOfDomain` (executor) covers (e): RESTRICT 2BP01, a mixed
  domain + composite DROP TYPE, CASCADE dropping the column but keeping the
  table, and IF EXISTS after the drop.

## Measurement

Regress runner, 19 suites. The diff line counts below compare three binaries:

- **base:** `8c1531f51`, before both commits;
- **HEAD:** `88ef78511`;
- **change:** HEAD + (e).

| suite | base | HEAD | change |
|---|---|---|---|
| privileges | 3925 | 3781 | 3781 |
| create\_operator | 243 | 196 | 196 |
| alter\_generic | 840 | 805 | 804 |
| domain | 1718 | 1718 | 1717 |
| polymorphism | 2238 | 2238 | 2236 |
| plpgsql | 4011 | 4012 | 3991 |
| jsonb | 6345 | not run | 6345 |

- The "privileges on types" block of privileges.sql now matches PG
  line for line.
- The plpgsql, polymorphism and alter\_generic movements between HEAD and
  change are run\-to\-run variance. plpgsql and alter\_generic are known
  nondeterministic cases. The polymorphism hunk is a `polyf` OUT\-parameter
  query that does not involve any type drop.
- The other 13 suites are line\-count identical: create\_type,
  create\_cast, create\_table, alter\_table, create\_aggregate, typed\_table,
  rowtypes, enum, create\_function\_sql, create\_procedure, dependency,
  object\_address, and jsonb (base to change).
- S9 census `routing-20261111a` on the `88ef78511` tree: 22/22, 99/99,
  99/99, IDENTICAL on all three corpora.

## Not covered (M0148\-0001j4)

- **OID order across catalogs.** Routine OIDs come from their own counter
  (create\_type myvarchar; typed\_table `persons, get_all_persons(),
  persons2`). Fixing this needs one allocator, like PG's
  `GetNewOidWithIndex`.
- **`pg_proc.proargtypes` is 0 for a composite\-array argument** (`st[]`).
- **`(s).total` on a composite SQL\-function parameter** fails with
  "applied to type unknown".
- **`CREATE TABLE … OF t (name WITH OPTIONS …)` repeating a column** is
  accepted. PG raises 42701.
- **Grouping sets with no sorted rollup** hash an ordered aggregate.
- **View columns over a type** depend on it through the `_RETURN` rule,
  which goopg does not model.
- **Other privileges hunks**, outside type USAGE:
  - the EXECUTE check on an array\-element coercion function
    (`'{1}'::int4[]::int8[]` as an ungranted user);
  - in the default\-privileges block, `has_type_privilege` and
    `has_function_privilege` calls fail with `function … does not exist`,
    although both are in pg\_proc. The cause is not diagnosed yet.
