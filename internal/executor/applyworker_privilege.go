package executor

// applyworker_privilege.go — the apply worker's identity and privilege checks
// (M0149-0057a, subscription/t/027_nosuperuser.pl).
//
// PG (worker.c, usercontext.c): with run_as_owner = false (the default, and
// the only mode goopg models) each change runs as the target table's owner.
// SwitchToUntrustedUser first requires that the subscription owner may SET
// ROLE to the table owner (member_can_set_role) — "role \"%s\" cannot SET
// ROLE to \"%s\"" otherwise — and stays the subscription owner when the table
// owner could in turn SET ROLE to it. TargetPrivilegesCheck then requires
// the action's privilege (INSERT / UPDATE / DELETE, and SELECT for the
// lookup an UPDATE or DELETE makes — FindReplTupleInLocalRel), reporting
// aclcheck_error's "permission denied for table %s", and refuses a table
// whose row-level security applies to that user: "user \"%s\" cannot
// replicate into relation with row-level security enabled: \"%s\"".
// maybe_reread_subscription restarts the worker when the subscription owner
// loses superuser.

import (
	"errors"
	"fmt"

	"github.com/goopg/goopg/internal/catalog"
)

// errApplyOwnerLostSuperuser makes the worker exit for a restart
// (maybe_reread_subscription's apply_worker_exit).
var errApplyOwnerLostSuperuser = errors.New("applyworker: subscription owner lost superuser; restarting")

// applySubscriptionOwner returns the subscription owner's OID (the
// bootstrap superuser when unknown).
func (w *ApplyWorker) applySubscriptionOwner() uint32 {
	if w.pubsub == nil || w.subName == "" {
		return catalog.BootstrapSuperuserOID
	}
	sub, ok := w.pubsub.LookupSubscription(w.subName, w.dbOid())
	if !ok || sub == nil || sub.Owner == 0 {
		return catalog.BootstrapSuperuserOID
	}
	return sub.Owner
}

// applyCheckOwnerSuperuser is maybe_reread_subscription's superuser check,
// run at each transaction start: the owner's superuser status is recorded
// the first time, and losing it LOGs PG's message and restarts the worker.
func (w *ApplyWorker) applyCheckOwnerSuperuser() error {
	im, ok := underlyingInMemory(w.cat)
	if !ok {
		return nil
	}
	now := im.IsSuperuser(w.applySubscriptionOwner())
	if !w.ownerSuperuserKnown {
		w.ownerSuperuserKnown, w.ownerSuperuser = true, now
		return nil
	}
	if w.ownerSuperuser && !now {
		w.log().Info(fmt.Sprintf("logical replication worker for subscription \"%s\" will restart because the subscription owner's superuser privileges have been revoked", w.subName))
		return errApplyOwnerLostSuperuser
	}
	w.ownerSuperuser = now
	return nil
}

// applyTableAccess is SwitchToUntrustedUser + TargetPrivilegesCheck for an
// applied change of kind action ("insert", "update" or "delete") on tbl.
func applyTableAccess(ctx *Context, w *ApplyWorker, tbl *catalog.Table, action string) error {
	im, ok := underlyingInMemory(w.cat)
	if !ok || tbl == nil {
		return nil
	}
	subOwner := w.applySubscriptionOwner()
	tblOwner := catalog.BootstrapSuperuserOID
	if tbl.Owner != "" {
		if oid, ok := im.RoleOID(tbl.Owner); ok {
			tblOwner = oid
		}
	}
	canSetRole := func(member, role uint32) bool {
		return member == role || im.IsSuperuser(member) || im.RoleIsMemberOf(member, role)
	}
	if !canSetRole(subOwner, tblOwner) {
		return &ExecError{Code: "42501", Message: fmt.Sprintf("role \"%s\" cannot SET ROLE to \"%s\"",
			im.RoleNameForOIDOrUnknown(subOwner), im.RoleNameForOIDOrUnknown(tblOwner))}
	}
	user := tblOwner
	if canSetRole(tblOwner, subOwner) {
		user = subOwner
	}
	if im.IsSuperuser(user) {
		return nil
	}
	name := im.RoleNameForOIDOrUnknown(user)
	privs := []string{"INSERT"}
	switch action {
	case "update":
		privs = []string{"UPDATE", "SELECT"}
	case "delete":
		privs = []string{"DELETE", "SELECT"}
	}
	saved := ctx.NonSuperuserRole
	ctx.NonSuperuserRole = name
	defer func() { ctx.NonSuperuserRole = saved }()
	for _, priv := range privs {
		if !dmlPrivilegePermittedAs(ctx, tbl, priv, name) {
			return &ExecError{Code: "42501", Message: fmt.Sprintf("permission denied for table %s", tbl.Name)}
		}
	}
	// check_enable_rls: row-level security applies to the table's owner
	// only when forced, and never to a BYPASSRLS role.
	if tbl.RowSecurity {
		attrs, _ := im.LookupRoleAttrs(name)
		applies := !attrs.BypassRLS && (user != tblOwner || tbl.ForceRowSecurity)
		if applies {
			return &ExecError{Code: "42501", Message: fmt.Sprintf("user \"%s\" cannot replicate into relation with row-level security enabled: \"%s\"", name, tbl.Name)}
		}
	}
	return nil
}
