package initdb

// GRANT/REVOKE role membership WAL replay (M0119-0004-ACLHEAP).
//
// Mirrors role_config_recovery.go's ALTER ROLE ... SET/RESET pattern:
// physical WAL replay ignores these record kinds (they carry only
// catalog.InMemory.roleMembers registry state, no on-disk page data), so
// this recovery pass walks the WAL once after physical replay finishes and
// re-applies each GRANT/REVOKE ROLE record so pg_auth_members reflects the
// pre-crash state after a restart.

import (
	"errors"
	"fmt"
	"os"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/wal"
)

// roleMembershipRegistryRecovery is the catalog-side surface this recovery
// pass needs. `*catalog.InMemory` satisfies it.
type roleMembershipRegistryRecovery interface {
	GrantRoleMembership(roleOid, memberOid, grantorOid uint32, admin, inherit, set *bool) uint32
	RevokeRoleMembership(roleOid, memberOid, grantorOid uint32, revokeOption string) bool
}

// replayRoleMembershipRecords reads every WAL record under walDir and
// applies GRANT/REVOKE role-membership entries to the catalog. A missing
// walDir means "freshly initdb'd cluster" and is treated as a no-op. The
// catalog argument may be nil (some embedded test setups), in which case
// the function returns nil without doing any I/O.
func replayRoleMembershipRecords(walDir string, cat catalog.Catalog) error {
	if cat == nil {
		return nil
	}
	reg, ok := cat.(roleMembershipRegistryRecovery)
	if !ok {
		// Catalog implementation does not expose the recovery hooks;
		// nothing to do. (catalog.InMemory does expose them.)
		return nil
	}
	if _, err := os.Stat(walDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat wal dir: %w", err)
	}

	records, err := wal.ReadAll(walDir, 0)
	if err != nil {
		return fmt.Errorf("read wal: %w", err)
	}

	for _, rec := range records {
		if len(rec.Payload) == 0 {
			continue
		}
		switch rec.Payload[0] {
		case wal.RecordKindGrantRoleMembership:
			roleOid, memberOid, grantorOid, admin, inherit, set, derr := wal.DecodeGrantRoleMembership(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode grant-role-membership at lsn %d: %w", rec.StartLSN, derr)
			}
			reg.GrantRoleMembership(roleOid, memberOid, grantorOid, admin, inherit, set)
		case wal.RecordKindRevokeRoleMembership:
			roleOid, memberOid, grantorOid, revokeOption, derr := wal.DecodeRevokeRoleMembership(rec.Payload)
			if derr != nil {
				return fmt.Errorf("decode revoke-role-membership at lsn %d: %w", rec.StartLSN, derr)
			}
			reg.RevokeRoleMembership(roleOid, memberOid, grantorOid, revokeOption)
		}
	}
	return nil
}
