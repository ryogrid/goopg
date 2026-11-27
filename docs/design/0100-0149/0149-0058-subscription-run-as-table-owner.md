# M0149\-0058 — Port subscription/t/033\_run\_as\_table\_owner.pl

Status: done. TestPort\_Subscription033RunAsTableOwner passes 3/3 in all three pairings \(all\-goopg; PG 18.3
publisher → goopg subscriber; goopg publisher → PG 18.3 subscriber\).

033 runs a subscription with `run_as_owner = true`, so the apply worker acts as the subscription owner rather
than switching to the table owner. It walks the owner through several privilege states:

- no privileges: fails;
- INSERT only: INSERT replicates, UPDATE / DELETE fail for lack of SELECT;
- all privileges: replicates;
- only a SET\-but\-not\-INHERIT membership in the table owner: fails, because run\_as\_owner does not switch;
- an INHERIT membership: replicates;
- `ALTER SUBSCRIPTION … SET (run_as_owner = false)`: replicates through SET ROLE.

A last subscription, owned by a non\-superuser with a SET\-only membership, must do its initial copy as the table
owner. The goopg publisher pairing passed as written; the goopg subscriber needed M0149\-0058a.

## Landed — M0149\-0058a

- **run\_as\_owner.** `Subscription.RunAsOwner` \(pg\_subscription.subrunasowner\) is set by CREATE and ALTER … SET,
  journaled, reloaded and shown. The apply worker reads it at each change \(`applyRunAsOwner`\).
- **member\_can\_set\_role.** `catalog.MemberCanSetRole` follows only memberships carrying the SET option
  \(roles\_is\_member\_of with ROLERECURSE\_SETROLE\). `applyTableAccess` uses it for SwitchToUntrustedUser's check, and
  for its "the table owner may SET ROLE back, so stay" shortcut. The earlier RoleIsMemberOf ignored the option.
- **Owner privileges through INHERIT.** pg\_class\_aclmask grants a role the owner's implicit privileges when
  `has_privs_of_role(role, owner)`. `dmlPrivilegePermittedAs` now does the same \(`roleHasPrivsOfOwner`\), and
  check\_enable\_rls's owner test \(object\_ownercheck\) uses HasPrivsOfRole.
- **Initial copy.** goopg's table sync does not check privileges, so it needs no change for the last step. PG
  runs it as the table owner \(ledgered\).

Test: TestMemberCanSetRole.
