# M0146-0158 — superuser-only parameters need SET privilege

Status: landed 2026-10-12.
Parent: M0146-0155 (item 2a).

## Problem

A non-superuser role could SET, SET LOCAL, RESET or `set_config()` any
PGC\_SUSET parameter. For example, `SET ROLE plain; SET log_min_messages =
debug1` succeeded. A non-superuser could also attach such a parameter to a
routine with CREATE / ALTER FUNCTION … SET, and with M0146-0155 now applying
SET clauses at call time, that clause would run for every caller.

## PostgreSQL's rule

`set_config_option` (guc.c) is called with the context `superuser() ?
PGC_SUSET : PGC_USERSET`. A PGC\_SUSET parameter set from the USERSET context
raises 42501 `permission denied to set parameter "x"`, unless the role holds
SET on the parameter (`pg_parameter_aclcheck(..., ACL_SET)`). Every path
applies it:

- SET, SET LOCAL, RESET, and SET … TO DEFAULT;
- `set_config()`;
- a routine's SET clause. `fmgr_security_definer` passes the context after
  switching to the definer's user, so a superuser-owned definer may apply
  one for any caller, and a non-superuser-owned one may not;
- CREATE / ALTER FUNCTION … SET (`validate_option_array_item`).

RESET ALL (`ResetAllOptions`) bypasses `set_config_option` and resets every
parameter without the check.

## Design

The check belongs in the session registry: SET reaches it from the executor
and from the postmaster's string-level simple-query and extended-protocol
paths.

- **The registry hook.** `SessionRegistry.SetPrivilegeHook(currentRole,
  mayGrant)` installs the check.
  - The postmaster installs it when the connection is set up.
    `currentRole` is `connTx.NonSuperuserRole` (`""` while a superuser is
    effective).
  - `mayGrant` is `executor.ParameterSetAllowed`: the role is a superuser,
    or holds SET in `pg_parameter_acl`.
- **Where it applies.**
  - `checkSetPrivilege` runs in `prepareSet`, which covers `Set`,
    `CheckSet` and `PushSaveLevel`.
  - It runs in `Reset` as well, but not in `ResetAll`.
  - Only PGC\_SUSET variables are checked.
- **The definer override.** `PushRole` / `PopRole` keep an override stack.
  `enterSecurityDefiner` pushes the owner's role (`""` for a superuser
  owner) through `Context.PushSessionRole` before the routine's SET clauses
  apply, and pops it on exit.
- **CREATE / ALTER FUNCTION / PROCEDURE … SET.**
  `flattenFunctionConfigOps` checks every named entry through
  `Context.CheckSetPrivilege` (`SessionRegistry.CheckSetPrivilege`).
- **SQLSTATEs.**
  - `set_config()` maps a typed GUC error to its own SQLSTATE with no
    position, as `set_config_by_name` reports it. It used to report every
    failure as 22023.
  - Call-time SET-clause errors carry no position either (ProcessGUCArray).
  - The postmaster's RESET paths keep a typed error's SQLSTATE and still
    report 42704 for an unknown name.

## Verification

- **PG 18.3 probes.** Both are byte-identical to PG, apart from the
  non-superuser login case below.
  - Probe 1: SET / SET LOCAL / RESET / `set_config` as a non-superuser; a
    user-settable parameter; superuser- and non-superuser-owned definer and
    invoker routines with a SUSET clause; GRANT / REVOKE SET ON PARAMETER;
    a superuser role.
  - Probe 2: CREATE / ALTER FUNCTION … SET as a non-superuser, SET … TO
    DEFAULT, RESET ALL, and SET SESSION AUTHORIZATION.
- **Tests.**
  - `TestSetPrivilegeSuset` (misc): every path, the override stack, the
    grant, and RESET ALL.
  - `TestSusetParameterNeedsPrivilege` (postmaster). It fails on HEAD in
    every arm.
- **Regress A/B** over 10 GUC / privilege / function cases: identical,
  except `plpgsql` and `alter_generic`, which change by one line each and
  move between runs of the same binary (run context).
- **Gates.** Units; executor / postmaster / misc; the pg\_dump, per-database
  and `TestE2E_*` testport families; tpch-spotcheck, the acceptance arm and
  sf025 PASS=99.

## Residuals

- **Filed as S2 M0146-0162: a non-superuser who logs in directly runs with
  superuser rights.** `connTx.NonSuperuserRole` is set only by SET ROLE /
  SET SESSION AUTHORIZATION, so a `LOGIN` role connecting as itself bypasses
  table privilege checks: it reads a table it has no grant on. It also
  bypasses this parameter check. This has been ledgered since 2026-07-09 as
  a systemic "actingRole RBAC gap", but no task was filed.
- **Ledgered:**
  - The SET grant is not inherited through role membership
    (`has_param_privilege` honours inheritance).
  - ALTER FUNCTION … RESET ALL by a non-superuser should drop only the
    entries it may reset (GUCArrayReset).
  - Startup-packet options are not checked against the login role.
