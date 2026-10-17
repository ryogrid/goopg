package misc

import "fmt"

// SetPrivilegeHook installs set_config_option's PGC_SUSET check (guc.c):
// a PGC_SUSET parameter may be SET, SET LOCAL, RESET or applied from a
// routine's SET clause only by a superuser, or by a role holding SET on it
// (pg_parameter_acl). currentRole returns the session's effective role, ""
// when it is a superuser; mayGrant reports whether a role is a superuser or
// holds SET on the parameter. With no hook installed (embedded sessions,
// tests) nothing is checked. M0146-0158.
func (s *SessionRegistry) SetPrivilegeHook(currentRole func() string, mayGrant func(role, name string) bool) {
	s.currentRole, s.mayGrant = currentRole, mayGrant
}

// PushRole makes role the effective role for the privilege check until the
// matching PopRole: a SECURITY DEFINER routine runs, and applies its SET
// clauses, with its owner's rights (fmgr_security_definer switches the user
// before ProcessGUCArray). "" stands for a superuser owner.
func (s *SessionRegistry) PushRole(role string) {
	s.roleStack = append(s.roleStack, role)
}

// PopRole undoes the latest PushRole.
func (s *SessionRegistry) PopRole() {
	if n := len(s.roleStack); n > 0 {
		s.roleStack = s.roleStack[:n-1]
	}
}

// CheckSetPrivilege reports the error SET name would raise for lack of
// privilege, without setting anything — CREATE / ALTER FUNCTION ... SET
// validates a clause this way (validate_option_array_item). An unknown name
// passes; the value check reports it.
func (s *SessionRegistry) CheckSetPrivilege(name string) error {
	v, ok := s.lookupVariable(name)
	if !ok {
		return nil
	}
	return s.checkSetPrivilege(v)
}

func (s *SessionRegistry) checkSetPrivilege(v *Variable) error {
	if v.Context != ContextSuset || s.currentRole == nil {
		return nil
	}
	role := s.currentRole()
	if n := len(s.roleStack); n > 0 {
		role = s.roleStack[n-1]
	}
	if role == "" || (s.mayGrant != nil && s.mayGrant(role, v.Name)) {
		return nil
	}
	return &ValidationError{Code: "42501", Msg: fmt.Sprintf("permission denied to set parameter %q", v.Name)}
}
