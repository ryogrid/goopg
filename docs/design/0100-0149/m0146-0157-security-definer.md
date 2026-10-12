# M0146-0157 — a SECURITY DEFINER routine runs as its owner

Status: landed 2026-10-12.
Parent: M0146-0155 (item 2a).

## Problem

goopg stored `prosecdef` but never switched users for the call. Inside a
SECURITY DEFINER routine, `current_user` was the caller and every privilege
check used the caller's rights.

- A definer function meant to expose a table the caller cannot read failed
  with `permission denied`.
- A definer trigger function could not write to a table only its owner may
  write to.

Two further gaps made a correct switch impossible, or unsafe:

1. **The owner was never recorded.** CREATE FUNCTION / PROCEDURE stored
   `Owner = 0`, so `proowner` read as the bootstrap superuser whoever created
   the routine. Switching to that "owner" would have run every definer
   routine as superuser, which is a privilege escalation.
2. **Procedures lost `SECURITY DEFINER`.** The parser dropped it: the yacc
   `applyProcAttrs` and the legacy `parseCreateProcedureTail` both discarded
   it. PostgreSQL records `prosecdef` for procedures too.

## PostgreSQL's mechanism

For a routine with `prosecdef` (or proconfig), `fmgr_security_definer`
(fmgr.c) does the following:

- **Switch user.** It saves the current user and security context, then calls
  `SetUserIdAndSecContext(proowner, save_sec_context |
  SECURITY_LOCAL_USERID_CHANGE)`. `session_user` is unchanged.
- **Apply SET clauses.** It applies proconfig after the switch, so a SUSET
  check sees the owner's superuser-ness.
- **Restore.** It restores the user on return. On error the (sub)transaction
  abort restores it.

While `SECURITY_LOCAL_USERID_CHANGE` is set (`InLocalUserIdChange`),
`set_config_option` refuses GUC\_NOT\_WHILE\_SEC\_REST parameters (`role` and
`session_authorization`) with 42501 `cannot set parameter "role" within
security-definer function`.

When CREATE FUNCTION runs, `ProcedureCreate` records `proowner =
GetUserId()`. `CREATE OR REPLACE` keeps the existing `proowner`.

## Design

- **`Context.SecurityDefinerUser`.** It holds the owner's name while a definer
  body runs.
  - `EffectiveUserName` (current\_user / current\_role / user) returns it
    ahead of SET ROLE and the session user.
  - `SessionUserName` is untouched.
- **`enterRoutineConfig` is now the whole `fmgr_security_definer`.**
  `enterSecurityDefiner` runs first, then the M0146-0155 GUC save level. The
  combined restore pops the GUC level and then restores the user, on every
  exit path. The switch:
  - resolves the owner's name (`RoleNameAtOID`);
  - sets `SecurityDefinerUser`;
  - sets `NonSuperuserRole` to the owner, or to `""` for a superuser owner.
    Every privilege check already keys on `NonSuperuserRole`.

  Because the helper already sits at every place a body runs (scalar
  functions, both SRF paths, SQL and plpgsql procedures, trigger bodies), the
  switch reaches all of them. Child contexts copy the switched fields.
- **SET ROLE / RESET ROLE / SET and RESET SESSION AUTHORIZATION** inside a
  definer body raise PG's 42501.
- **Owner recording.** `execCreateFunction` and `execCreateProcedure` set
  `Owner = currentDDLOwnerOID()`, the effective role's OID.
  `Routines.Create` keeps the existing owner on OR REPLACE.
- **Procedures keep `SECURITY DEFINER`.**
  - `applyProcAttrs` copies `securityDefiner`.
  - The legacy procedure loop parses `[EXTERNAL] SECURITY DEFINER | INVOKER`.
  - The parity-goldens diff is the intended flip of that one record's
    `SecurityDefiner` to `true`.

## Verification

- **PG 18.3 probes.**
  - Probe 1 is a definer and an invoker owned by a non-superuser, a table
    readable only by the owner, plpgsql nesting into an invoker function, an
    error raised inside a definer, a definer whose error is caught by an
    exception block, a definer owned by a superuser, and a definer trigger
    writing to a table the caller cannot write.
  - Probe 2 is SQL and plpgsql definer procedures, SET ROLE / RESET ROLE
    inside, definer combined with SET, OR REPLACE keeping the owner, ALTER
    … SECURITY DEFINER, and ALTER … OWNER TO.
  - Both match PG except the pre-existing error-position and plpgsql CONTEXT
    rendering.
- **Restarts.** Owners and `prosecdef` survive two restarts, and the definer
  still runs as its owner afterwards.
- **Tests.**
  - `TestSecurityDefinerRunsAsOwner` (postmaster) fails on HEAD in every arm.
  - `TestRoutinesReplaceKeepsOwner` (catalog).
- **Regress A/B.**
  - `privileges` 3879 → 3868. `priv_testfunc4` and `dogrant_ok` now run with
    the owner's rights.
  - `alter_generic` run alone differs from HEAD only by the function owners,
    which are now the creating roles, as in PG.
  - The other 13 cases are identical.
- **Gates.** Units; catalog / executor / postmaster / parser / initdb; the
  pg\_dump and per-database testport families; tpch-spotcheck, the
  acceptance arm and sf025 PASS=99.

## Residuals

- **Filed as S2 M0146-0161: table GRANTs (`relacl`) are lost on restart.**
  Measured at HEAD before this change: `GRANT SELECT ON t TO r` followed by a
  restart leaves `relacl` empty. Grants vanish, and REVOKEs from PUBLIC
  revert.
- **Ledgered:**
  - ALTER FUNCTION / PROCEDURE skip the "must be owner of function" and
    role-membership checks (`alter_generic`), and so does CREATE OR REPLACE by
    a non-owner. These are now implementable, since `proowner` is real.
  - There is no `SECURITY_RESTRICTED_OPERATION`. Maintenance commands such as
    `REFRESH`, `VACUUM` and `ANALYZE` with index expressions do not switch to
    the table owner or restrict SET ROLE.
  - `set_config('role', …)` inside a definer is not refused. Only the SET /
    RESET statements are.
  - CREATE AGGREGATE does not record its owner.
