package catalog

import "strings"

// GrantOptionToPublic reports whether a GRANT would give WITH GRANT OPTION to
// PUBLIC, which PostgreSQL forbids for every object class:
// merge_acl_with_grant (./postgres/src/backend/catalog/aclchk.c:208) raises
// 0LP01 "grant options can only be granted to roles" because a privilege a
// user re-granted while holding it only through PUBLIC could never be cleaned
// up when that user is dropped. Every ExecGrant_* class and ALTER DEFAULT
// PRIVILEGES pass through that one function, so goopg's grant paths share this
// one predicate. REVOKE is unaffected (is_grant is false there).
//
// A grantee is PUBLIC when it spells the keyword: an unquoted `public` in any
// case, or the quoted lower-case `"public"` — gram.y's RoleSpec maps any
// NonReservedWord equal to "public" to ROLESPEC_PUBLIC, and a quoted
// identifier keeps its case, so `"PUBLIC"` names an ordinary role.
func GrantOptionToPublic(withGrantOption bool, grantees []string) bool {
	if !withGrantOption {
		return false
	}
	for _, g := range grantees {
		g = strings.TrimSpace(g)
		if len(g) >= 2 && g[0] == '"' && g[len(g)-1] == '"' {
			if g[1:len(g)-1] == "public" {
				return true
			}
			continue
		}
		if strings.EqualFold(g, "public") {
			return true
		}
	}
	return false
}

// GrantOptionToPublicMessage is PostgreSQL's error text for the refusal.
const GrantOptionToPublicMessage = "grant options can only be granted to roles"
