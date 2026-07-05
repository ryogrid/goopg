package initdb

// Startup side of the role/auth persistent store (root-0021): read the
// pg_authid heap file back into the catalog role registry. The write side
// (executor.SyncPgAuthidFile) and the shared reader (executor.ReadPgAuthidRows)
// live in internal/executor so both role-DDL paths — the server-side
// intercept for CREATE/ALTER ROLE (which the parser cannot parse) and the
// executor's execDropCompat role arm (DROP ROLE parses as a generic
// DropStmt) — reach the same persistence without an import cycle
// (initdb imports executor, never the reverse).

import (
	"fmt"
	"strings"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
)

// LoadRolesFromAuthidHeap reads global/1260 back into the catalog role
// registry at startup: user roles are re-registered with their persisted
// OIDs and attributes; the bootstrap superuser's verifier is recorded in the
// attrs sidecar (SetRoleAttrs special-cases "postgres") so cmd/goopg can
// seed the auth UserStore; predefined pg_* roles stay heap-only, exactly as
// before. Runs before replayRoleDDLRecords (heap base, WAL tail).
func LoadRolesFromAuthidHeap(dataDir string, cat catalog.Catalog) error {
	im, ok := cat.(*catalog.InMemory)
	if dataDir == "" || !ok || im == nil {
		return nil
	}
	rows, err := executor.ReadPgAuthidRows(dataDir)
	if err != nil {
		return fmt.Errorf("pg_authid load: %w", err)
	}
	for _, d := range rows {
		lower := strings.ToLower(d.Rolname)
		attrs := catalog.RoleAttrs{
			CanLogin:    d.CanLogin,
			Superuser:   d.Super,
			CreateDB:    d.CreateDB,
			CreateRole:  d.CreateRole,
			Replication: d.Replication,
			BypassRLS:   d.BypassRLS,
			ConnLimit:   d.ConnLimit,
			ValidUntil:  d.ValidUntil,
		}
		if d.HasPasswd {
			switch {
			case strings.HasPrefix(d.Password, "SCRAM-SHA-256$"):
				attrs.CredType = 3
			case len(d.Password) == 35 && strings.HasPrefix(d.Password, "md5"):
				attrs.CredType = 2
			default:
				attrs.CredType = 1
			}
			attrs.Secret = d.Password
		}
		switch {
		case d.OID == 10 || lower == "postgres":
			im.SetRoleAttrs("postgres", attrs)
		case strings.HasPrefix(lower, "pg_"):
			// predefined — heap-only, not a registry role
		default:
			im.RegisterRoleWithOID(lower, d.OID)
			im.SetRoleAttrs(lower, attrs)
		}
	}
	return nil
}
