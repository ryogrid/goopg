package postmaster

// role_ddl.go — in-process CREATE/ALTER/DROP ROLE handler (M0095-0006;
// restart persistence + credentials root-0021).
//
// goopg's parser does not yet include a full CREATE/DROP ROLE grammar, so
// these statements reach the wire layer as parse failures. tryHandleRoleDDL
// intercepts them before the generic compatNoopCommandTag path so that:
//   - CREATE ROLE / CREATE USER / CREATE GROUP: registers the role in the
//     server's in-memory role set (Server.roles) and the catalog role
//     registry, parses the attribute list (PASSWORD, LOGIN/NOLOGIN,
//     SUPERUSER/NOSUPERUSER), and mirrors the credential into the live auth
//     UserStore so password/md5/scram logins authenticate immediately.
//   - ALTER ROLE / ALTER USER (attribute form): applies the same attribute
//     parsing to an existing role.
//   - ALTER ROLE / ALTER USER ... RENAME TO: re-keys the role registry entry
//     (preserving its OID) and the live auth credential (root-0021 follow-up).
//   - DROP ROLE / DROP USER / DROP GROUP: unregisters everywhere.
//
// Persistence mirrors PostgreSQL's pg_authid model (base store + WAL tail):
// each mutation (a) appends a RecordKindRoleState/DropRole WAL record — the
// crash-recovery tail — and (b) rebuilds the pg_authid heap file
// (global/1260) via initdb.SyncPgAuthidFile — the durable base that survives
// checkpoint-driven WAL segment pruning. Startup loads the heap base first
// (LoadRolesFromAuthidHeap), then replays newer WAL records on top.
//
// Passwords are never persisted in cleartext: `PASSWORD 'x'` becomes an
// upstream-format SCRAM-SHA-256 verifier (auth.NewSCRAMSecret — the same
// generator initdb uses for the bootstrap superuser's rolpassword),
// mirroring PG's encrypt_password (postgres/src/backend/libpq/crypt.c) under
// the default password_encryption = 'scram-sha-256'. A pre-computed
// SCRAM-SHA-256$… or md5<hex> secret is stored verbatim, exactly like PG.

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/libpq/auth"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/libpq"
	"github.com/goopg/goopg/internal/utils/errcodes"
)

// tryHandleRoleDDL returns (handled, err). dbName is the calling
// connection's own database (connTx.DBName) — needed only for the ALTER
// ROLE ... [IN DATABASE ...] SET/RESET branch, mirroring
// tryHandleDatabaseDDL's liveDBName parameter.
//   - handled=true, err=nil:   statement was handled successfully
//   - handled=true, err!=nil:  statement was handled but failed (e.g. role not found)
//   - handled=false, err=nil:  not a role DDL statement; caller should continue
// actingRole is variadic (not a plain trailing string) so the ~30 existing
// direct-call test sites that predate the M0134-0114 privilege-attribute
// checks below keep compiling unchanged — omitting it defaults to "", the
// connTx.NonSuperuserRole convention for "the bootstrap superuser", which
// bypasses every check exactly as those tests' pre-existing behavior
// assumed. Only the wire-dispatch call sites (dispatch.go,
// dispatch_extended.go) pass the caller's real effective role.
func (s *Server) tryHandleRoleDDL(sql string, dbName string, resolveCurrent currentGUCResolver, actingRole ...string) (bool, error) {
	acting := ""
	if len(actingRole) > 0 {
		acting = actingRole[0]
	}
	norm := normalizeCompatSQL(sql)
	switch {
	case strings.HasPrefix(norm, "create role "), strings.HasPrefix(norm, "create user "),
		strings.HasPrefix(norm, "create group "):
		name := roleNameFromCreate(norm)
		if name == "" {
			return false, nil // malformed; let caller handle
		}
		// CreateRole (postgres/src/backend/commands/user.c) rejects the
		// reserved "pg_" namespace before it even opens pg_authid — i.e.
		// before the duplicate-name check below, matching PG's error
		// precedence for `CREATE ROLE pg_x` even when pg_x already exists.
		if isReservedRoleName(name) {
			return true, reservedRoleNameErr(name)
		}
		// PG defaults: CREATE USER implies LOGIN; CREATE ROLE/GROUP imply
		// NOLOGIN (postgres/src/backend/commands/user.c CreateRole).
		// Explicit LOGIN/NOLOGIN below overrides.
		attrs := catalog.RoleAttrs{CanLogin: strings.HasPrefix(norm, "create user "), ConnLimit: -1}
		applyRoleAttrOptions(sql, norm, &attrs, resolveCurrent)
		if acting != "" {
			if im, ok := s.cfg.Catalog.(*catalog.InMemory); ok {
				if err := checkCreateRolePrivileges(im, acting, attrs); err != nil {
					return true, err
				}
			}
		}
		s.registerRole(name)
		// Also register in catalog so executor-level DROP ROLE IF EXISTS can check.
		if s.cfg.Catalog != nil {
			s.cfg.Catalog.RegisterRole(name)
			if im, ok := s.cfg.Catalog.(*catalog.InMemory); ok {
				im.SetRoleAttrs(name, attrs)
			}
		}
		s.applyRoleCredential(name, attrs)
		if err := s.persistRoleState(name, attrs); err != nil {
			// Roll back the registrations so memory and disk agree
			// (mirrors tryHandleDatabaseDDL's rollback-on-append-failure).
			_ = s.unregisterRole(name, true)
			if s.cfg.Catalog != nil {
				s.cfg.Catalog.UnregisterRole(name)
			}
			s.removeRoleCredential(name)
			return true, err
		}
		return true, nil

	case strings.HasPrefix(norm, "alter role "), strings.HasPrefix(norm, "alter user "):
		if op, ok := parseAlterRoleConfig(sql); ok {
			return s.applyAlterRoleConfig(op, dbName, resolveCurrent)
		}
		if oldName, newName, ok := roleRenameFromAlter(norm); ok {
			return true, s.renameRole(oldName, newName)
		}
		name, hasAttrs := roleNameFromAlter(norm)
		if name == "" || !hasAttrs {
			// Not the attribute form (e.g. SET/RESET guc) — leave it to the
			// pre-existing compat no-op path.
			return false, nil
		}
		isBootstrap := strings.EqualFold(name, "postgres")
		if !s.roleExists(name) && !isBootstrap {
			return true, roleDoesNotExistErr(name)
		}
		// Start from the recorded attributes so an ALTER only changes what it
		// names (PG semantics: unspecified attributes keep their value). The
		// bootstrap superuser defaults to superuser+login.
		attrs := catalog.RoleAttrs{CanLogin: isBootstrap, Superuser: isBootstrap, ConnLimit: -1}
		im, isInMem := s.cfg.Catalog.(*catalog.InMemory)
		if isInMem {
			if cur, found := im.LookupRoleAttrs(name); found {
				attrs = cur
			}
		}
		applyRoleAttrOptions(sql, norm, &attrs, resolveCurrent)
		if acting != "" && isInMem {
			if err := checkAlterRoleAttrPrivileges(im, acting, norm); err != nil {
				return true, err
			}
		}
		if isInMem {
			im.SetRoleAttrs(name, attrs)
		}
		s.applyRoleCredential(name, attrs)
		if err := s.persistRoleState(name, attrs); err != nil {
			return true, err
		}
		return true, nil

	case strings.HasPrefix(norm, "drop role "), strings.HasPrefix(norm, "drop user "),
		strings.HasPrefix(norm, "drop group "):
		name, ifExists := roleNameFromDrop(norm)
		if name == "" {
			return false, nil // malformed; let caller handle
		}
		// Capture the OID before the registry removal — the heap DELETE
		// (B4.5) stamps the pg_authid row by oid.
		var oid uint32
		if im, ok := s.cfg.Catalog.(*catalog.InMemory); ok {
			oid, _ = im.RoleOID(name)
		}
		if err := s.unregisterRole(name, ifExists); err != nil {
			return true, err
		}
		// Also unregister from catalog.
		if s.cfg.Catalog != nil {
			s.cfg.Catalog.UnregisterRole(name)
		}
		s.removeRoleCredential(name)
		if err := s.deleteAuthidHeapRow(oid); err != nil {
			return true, err
		}
		return true, nil
	}
	return false, nil
}

// persistRoleState makes a role mutation (CREATE/ALTER) durable by journaling
// the role's current pg_authid heap row (B4.5): a real XLOG_HEAP_INSERT on
// global/1260 that a PG standby replays, replacing the retired whole-file
// SyncPgAuthidFile + RecordKindRoleState(67). See the file header.
func (s *Server) persistRoleState(name string, attrs catalog.RoleAttrs) error {
	var oid uint32
	if im, ok := s.cfg.Catalog.(*catalog.InMemory); ok {
		oid, _ = im.RoleOID(name)
	}
	return s.syncAuthidHeapRow(strings.ToLower(name), oid, attrs)
}

// syncAuthidHeapRow re-syncs the single pg_authid row for a role (CREATE/ALTER/
// RENAME) from its own short-lived transaction, mirroring B4.2's
// syncDbRoleSettingHeap. pg_authid is SHARED (global/1260); the executor writer
// stamps the old row + writes the current state + maintains the 2676/2677
// indexes.
func (s *Server) syncAuthidHeapRow(rolname string, oid uint32, attrs catalog.RoleAttrs) error {
	if oid == 0 {
		return nil
	}
	secret := ""
	if attrs.CredType != 0 {
		secret = attrs.Secret
	}
	return s.runAuthidHeapTxn(func(ectx *executor.Context) error {
		return executor.SyncAuthidRow(ectx, oid, rolname, attrs.Superuser, attrs.CanLogin,
			attrs.CreateDB, attrs.CreateRole, attrs.Replication, attrs.BypassRLS,
			attrs.ConnLimit, secret, attrs.ValidUntil)
	})
}

// deleteAuthidHeapRow stamps the pg_authid row for oid deleted (DROP ROLE) from
// its own short-lived transaction (a real XLOG_HEAP_DELETE on global/1260).
func (s *Server) deleteAuthidHeapRow(oid uint32) error {
	if oid == 0 {
		return nil
	}
	return s.runAuthidHeapTxn(func(ectx *executor.Context) error {
		return executor.DeleteAuthidRow(ectx, oid)
	})
}

// runAuthidHeapTxn opens a short-lived internal transaction wired for catalog
// heap writes and runs fn against it, committing on success. Mirrors B4.2's
// syncDbRoleSettingHeap scaffolding (own Begin/Snapshot/Commit). A missing
// Pool/TxnMgr (or a catalog with no on-disk pg_attribute, e.g. an in-memory
// test harness) is a no-op — the registry already holds the runtime truth.
func (s *Server) runAuthidHeapTxn(fn func(*executor.Context) error) error {
	if s.cfg.Pool == nil || s.cfg.TxnMgr == nil {
		return nil
	}
	ectx := executor.NewContext()
	ectx.Pool = s.cfg.Pool
	ectx.Catalog = s.cfg.Catalog
	ectx.CurrentDatabaseOid = catalog.DefaultDBOid // resolve pg_attribute for the heap-sync guard
	if !executor.CatalogHeapSyncAvailable(ectx) {
		return nil
	}
	tx, err := s.cfg.TxnMgr.Begin(transam.IsolationReadCommitted)
	if err != nil {
		return fmt.Errorf("begin pg_authid sync transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = s.cfg.TxnMgr.Rollback(tx)
		}
	}()
	snap, err := s.cfg.TxnMgr.SnapshotFor(tx)
	if err != nil {
		return fmt.Errorf("snapshot for pg_authid sync transaction: %w", err)
	}
	ectx.TxnMgr = s.cfg.TxnMgr
	ectx.Tx = tx
	ectx.Snap = snap
	ectx.WAL = s.cfg.WAL
	if err := fn(ectx); err != nil {
		return err
	}
	if err := s.cfg.TxnMgr.Commit(tx); err != nil {
		return fmt.Errorf("commit pg_authid sync transaction: %w", err)
	}
	committed = true
	return nil
}

// renameRole implements `ALTER ROLE/USER <name> RENAME TO <newname>`,
// mirroring PostgreSQL's RenameRole (postgres/src/backend/commands/user.c):
// role-exists (42704), reserved-"pg_"-prefix on the new name (42939), and
// new-name-already-exists (42710) checks, then re-keys the catalog role
// registry (preserving the OID), the connection-time role set, the live auth
// credential, and appends the WAL rename record. root-0021 follow-up
// (M0119-0004). Not modelled here (see the deferral ledger): PG's
// "session/current user cannot be renamed" guard, which needs per-connection
// session-role context this SQL-string-level handler does not have, and the
// superuser-may-only-rename-superuser privilege check, which — like every
// other role-DDL privilege check in this handler — is accept-and-ignore
// today.
func (s *Server) renameRole(name, newName string) error {
	isBootstrap := strings.EqualFold(name, "postgres")
	if !s.roleExists(name) && !isBootstrap {
		return roleDoesNotExistErr(name)
	}
	if isBootstrap {
		// The bootstrap superuser's identity is hardcoded to the literal
		// name "postgres" in several places (RoleOID, initdb's pg_authid
		// seeding); renaming it away is a structural change out of this
		// slice's scope, not just a persistence gap.
		return &roleError{code: errcodes.FeatureNotSupported, msg: "cannot rename the bootstrap superuser"}
	}
	if isReservedRoleName(newName) {
		return reservedRoleNameErr(newName)
	}
	if s.roleExists(newName) || strings.EqualFold(newName, "postgres") {
		return roleAlreadyExistsErr(newName)
	}
	im, isInMem := s.cfg.Catalog.(*catalog.InMemory)
	if isInMem && !im.RenameRole(name, newName) {
		return roleDoesNotExistErr(name)
	}
	s.rolesMu.Lock()
	delete(s.roles, name)
	s.roles[strings.ToLower(newName)] = struct{}{}
	s.rolesMu.Unlock()
	if store, ok := s.cfg.UserStore.(*auth.MapUserStore); ok && store != nil {
		if cred, found := store.Lookup(strings.ToLower(name)); found {
			store.Set(strings.ToLower(newName), cred)
			store.Remove(strings.ToLower(name))
		}
	}
	// Re-sync the pg_authid row by its (preserved) OID: the writer stamps the
	// old-name row and writes the row under the new rolname (B4.5). A RENAME is
	// just an attribute change that happens to touch rolname, so it rides the
	// same per-row path as CREATE/ALTER.
	var oid uint32
	attrs := catalog.RoleAttrs{ConnLimit: -1}
	if isInMem {
		oid, _ = im.RoleOID(newName)
		if a, ok := im.LookupRoleAttrs(newName); ok {
			attrs = a
		}
	}
	return s.syncAuthidHeapRow(strings.ToLower(newName), oid, attrs)
}

// isReservedRoleName mirrors PostgreSQL's IsReservedName (postgres/src/
// backend/commands/user.c): role names starting with "pg_" are reserved for
// system roles.
func isReservedRoleName(name string) bool {
	return strings.HasPrefix(strings.ToLower(name), "pg_")
}

// applyRoleAttrOptions scans the option list of a CREATE/ALTER ROLE statement
// and folds the recognised attributes into attrs. norm is the lower-cased
// normalised statement (keyword matching); sql is the ORIGINAL statement —
// the password literal (and the VALID UNTIL timestamp literal) must be taken
// from it because normalizeCompatSQL lower-cases the whole line and would
// corrupt a case-sensitive value. CREATEDB/CREATEROLE/REPLICATION/BYPASSRLS/
// CONNECTION LIMIT/VALID UNTIL are recognised (DU-002 slice 439 follow-up);
// IN ROLE/ADMIN/ROLE/USER/SYSID (membership + the legacy numeric-OID clause)
// remain unrecognised and ignored, matching the handler's historical
// accept-and-ignore behaviour for options outside RoleAttrs' scope.
func applyRoleAttrOptions(sql, norm string, attrs *catalog.RoleAttrs, resolveCurrent currentGUCResolver) {
	if strings.Contains(norm, " nosuperuser") {
		attrs.Superuser = false
	} else if strings.Contains(norm, " superuser") {
		attrs.Superuser = true
	}
	if strings.Contains(norm, " nologin") {
		attrs.CanLogin = false
	} else if strings.Contains(norm, " login") {
		attrs.CanLogin = true
	}
	if strings.Contains(norm, " nocreatedb") {
		attrs.CreateDB = false
	} else if strings.Contains(norm, " createdb") {
		attrs.CreateDB = true
	}
	if strings.Contains(norm, " nocreaterole") {
		attrs.CreateRole = false
	} else if strings.Contains(norm, " createrole") {
		attrs.CreateRole = true
	}
	if strings.Contains(norm, " noreplication") {
		attrs.Replication = false
	} else if strings.Contains(norm, " replication") {
		attrs.Replication = true
	}
	if strings.Contains(norm, " nobypassrls") {
		attrs.BypassRLS = false
	} else if strings.Contains(norm, " bypassrls") {
		attrs.BypassRLS = true
	}
	if n, ok := extractRoleConnLimit(norm); ok {
		attrs.ConnLimit = n
	}
	if v, ok := extractRoleValidUntil(sql, norm); ok {
		attrs.ValidUntil = v
	}
	if pw, kind, ok := extractRolePassword(sql, norm); ok {
		switch kind {
		case rolePasswordNull:
			attrs.CredType = 0
			attrs.Secret = ""
		case rolePasswordSCRAM:
			attrs.CredType = 3
			attrs.Secret = pw
		case rolePasswordMD5:
			attrs.CredType = 2
			attrs.Secret = pw
		default: // plaintext — shadow into a SCRAM verifier like PG's
			// encrypt_password under password_encryption='scram-sha-256'.
			sec, err := auth.NewSCRAMSecretWithIterations(pw, resolveScramIterations(resolveCurrent))
			if err != nil {
				return
			}
			attrs.CredType = 3
			attrs.Secret = sec.String()
		}
	}
}

// resolveScramIterations reads the calling session's live scram_iterations
// GUC (postgres/src/backend/commands/user.c CreateRole/AlterRole read the
// same setting when hashing a plaintext PASSWORD) so SET scram_iterations =
// N actually changes newly-derived verifiers' PBKDF2 cost, matching
// upstream. Falls back to auth's own default whenever no session/GUC is
// available or the stored value doesn't parse (auth.NewSCRAMSecretWithIterations
// applies the same non-positive fallback, so 0 here is safe).
func resolveScramIterations(resolveCurrent currentGUCResolver) int {
	if resolveCurrent == nil {
		return 0
	}
	val, ok := resolveCurrent("scram_iterations")
	if !ok {
		return 0
	}
	n, err := strconv.Atoi(val)
	if err != nil {
		return 0
	}
	return n
}

type rolePasswordKind int

const (
	rolePasswordPlain rolePasswordKind = iota
	rolePasswordNull
	rolePasswordMD5
	rolePasswordSCRAM
)

// extractRolePassword finds `[ENCRYPTED] PASSWORD 'secret'` (or PASSWORD
// NULL) in a CREATE/ALTER ROLE statement. The keyword is located on the
// normalised string; the literal's bytes are read from the ORIGINAL sql
// (case-preserved), handling doubled-single-quote escapes.
func extractRolePassword(sql, norm string) (secret string, kind rolePasswordKind, ok bool) {
	idx := strings.Index(norm, " password ")
	if idx < 0 {
		return "", 0, false
	}
	rest := strings.TrimSpace(norm[idx+len(" password "):])
	if rest == "null" || strings.HasPrefix(rest, "null ") || strings.HasPrefix(rest, "null;") {
		return "", rolePasswordNull, true
	}
	if !strings.HasPrefix(rest, "'") {
		return "", 0, false
	}
	// Locate the literal in the ORIGINAL sql: the first quote after the
	// (case-insensitive) password keyword.
	lowSQL := strings.ToLower(sql)
	kw := strings.Index(lowSQL, "password")
	if kw < 0 {
		return "", 0, false
	}
	open := strings.Index(sql[kw:], "'")
	if open < 0 {
		return "", 0, false
	}
	start := kw + open + 1
	// SQL single-quote escaping: '' inside the literal is a literal quote.
	var b strings.Builder
	i := start
	for i < len(sql) {
		if sql[i] == '\'' {
			if i+1 < len(sql) && sql[i+1] == '\'' {
				b.WriteByte('\'')
				i += 2
				continue
			}
			break
		}
		b.WriteByte(sql[i])
		i++
	}
	secret = b.String()
	switch {
	case strings.HasPrefix(secret, "SCRAM-SHA-256$"):
		return secret, rolePasswordSCRAM, true
	case len(secret) == 35 && strings.HasPrefix(secret, "md5"):
		return secret, rolePasswordMD5, true
	}
	return secret, rolePasswordPlain, true
}

// extractRoleConnLimit finds `CONNECTION LIMIT <n>` in a CREATE/ALTER ROLE
// statement's normalised text. n may be negative (PG allows CONNECTION LIMIT
// -1 to mean "no limit", the default). ok is false when the clause is absent
// or the following token is not an integer.
func extractRoleConnLimit(norm string) (n int32, ok bool) {
	idx := strings.Index(norm, " connection limit ")
	if idx < 0 {
		return 0, false
	}
	rest := strings.TrimSpace(norm[idx+len(" connection limit "):])
	end := strings.IndexAny(rest, " \t\n\r;,")
	if end >= 0 {
		rest = rest[:end]
	}
	v, err := strconv.ParseInt(rest, 10, 32)
	if err != nil {
		return 0, false
	}
	return int32(v), true
}

// extractRoleValidUntil finds `VALID UNTIL '<literal>'` in a CREATE/ALTER
// ROLE statement. The literal's bytes are read from the ORIGINAL sql
// (case-preserved), matching extractRolePassword's approach; single-quote
// doubling is unescaped the same way. `VALID UNTIL NULL` and `'infinity'`
// are both recognised — goopg stores the raw literal text verbatim and never
// evaluates it (no password-expiry enforcement), so both round-trip through
// pg_authid.rolvaliduntil as the text pg_dump emitted.
func extractRoleValidUntil(sql, norm string) (value string, ok bool) {
	idx := strings.Index(norm, " valid until ")
	if idx < 0 {
		return "", false
	}
	rest := strings.TrimSpace(norm[idx+len(" valid until "):])
	if rest == "null" || strings.HasPrefix(rest, "null ") || strings.HasPrefix(rest, "null;") {
		return "", true // NULL clears any previously-set expiration
	}
	if !strings.HasPrefix(rest, "'") {
		return "", false
	}
	lowSQL := strings.ToLower(sql)
	kw := strings.Index(lowSQL, "valid")
	if kw < 0 {
		return "", false
	}
	open := strings.Index(sql[kw:], "'")
	if open < 0 {
		return "", false
	}
	start := kw + open + 1
	var b strings.Builder
	i := start
	for i < len(sql) {
		if sql[i] == '\'' {
			if i+1 < len(sql) && sql[i+1] == '\'' {
				b.WriteByte('\'')
				i += 2
				continue
			}
			break
		}
		b.WriteByte(sql[i])
		i++
	}
	return b.String(), true
}

// applyRoleCredential mirrors the role's credential into the live auth
// UserStore so password/md5/scram logins authenticate immediately (the
// exchange in internal/auth consults cfg.UserStore). No-ops when the store
// is absent or not the mutable map implementation.
func (s *Server) applyRoleCredential(name string, attrs catalog.RoleAttrs) {
	store, ok := s.cfg.UserStore.(*auth.MapUserStore)
	if !ok || store == nil {
		return
	}
	if attrs.CredType == 0 || attrs.Secret == "" {
		store.Remove(strings.ToLower(name))
		return
	}
	if cred, err := auth.CredentialFromSecret(attrs.Secret); err == nil {
		store.Set(strings.ToLower(name), cred)
	}
}

// removeRoleCredential drops the role's credential from the live UserStore.
func (s *Server) removeRoleCredential(name string) {
	if store, ok := s.cfg.UserStore.(*auth.MapUserStore); ok && store != nil {
		store.Remove(strings.ToLower(name))
	}
}

// roleNameFromAlter extracts the role name from a normalised ALTER ROLE/USER
// statement and reports whether the statement is the attribute form this
// handler owns (PASSWORD/LOGIN/SUPERUSER/...). RENAME TO / SET / RESET / IN
// DATABASE forms return hasAttrs=false so the legacy compat no-op keeps
// handling them.
func roleNameFromAlter(norm string) (name string, hasAttrs bool) {
	var rest string
	switch {
	case strings.HasPrefix(norm, "alter role "):
		rest = strings.TrimSpace(norm[len("alter role "):])
	case strings.HasPrefix(norm, "alter user "):
		rest = strings.TrimSpace(norm[len("alter user "):])
	default:
		return "", false
	}
	name = extractFirstSQLIdent(norm, rest)
	if name == "" {
		return "", false
	}
	pos := strings.Index(rest, name)
	if pos < 0 {
		return "", false
	}
	opts := strings.TrimSpace(rest[pos+len(name):])
	if opts == "" || strings.HasPrefix(opts, "rename ") ||
		strings.HasPrefix(opts, "set ") || strings.HasPrefix(opts, "reset ") ||
		strings.HasPrefix(opts, "in database ") {
		return name, false
	}
	return name, true
}

// roleConfigRegistry is the subset of catalog.Catalog the ALTER ROLE ...
// SET/RESET handler needs beyond RoleOID (already exposed directly on the
// catalog.Catalog interface). catalog.InMemory satisfies this interface.
// M0119-0004-ACLHEAP (ALTER ROLE ... SET follow-up).
type roleConfigRegistry interface {
	SetRoleConfig(roleOid, dbOid uint32, name, value string)
	ResetRoleConfig(roleOid, dbOid uint32, name string)
	ResetAllRoleConfig(roleOid, dbOid uint32)
	// RoleConfigEntries returns (roleOid, dbOid)'s current setconfig list,
	// read after a mutation to re-sync the pg_db_role_setting heap row (B4.2).
	RoleConfigEntries(roleOid, dbOid uint32) []string
}

// alterRoleConfigOp is the result of a successful parseAlterRoleConfig
// classification: an `ALTER ROLE <name> [IN DATABASE <dbname>] SET <config>
// = <value>` / `RESET <config>` / `RESET ALL` statement. Mirrors
// parseAlterDatabaseConfig (internal/server/database_ddl.go) — same
// string-prefix wire-dispatch bypass, complementary setrole != 0 half of
// pg_db_role_setting. M0119-0004-ACLHEAP (ALTER ROLE ... SET follow-up).
type alterRoleConfigOp struct {
	roleName    string
	allRoles    bool // true for "ALTER ROLE ALL SET/RESET ..." (role_specification = ALL, an unquoted keyword — roleName is meaningless when this is set)
	hasDatabase bool // true when an "IN DATABASE <dbname>" clause was present
	dbName      string
	configName  string // empty when resetAll
	configValue string // meaningful only when !reset && !resetAll && !fromCurrent
	reset       bool   // RESET <name>
	resetAll    bool   // RESET ALL
	fromCurrent bool   // SET <name> FROM CURRENT — configValue resolved at apply time
}

// parseAlterRoleConfig recognises the SET/RESET forms of ALTER ROLE/USER
// described on alterRoleConfigOp, with an optional "IN DATABASE <dbname>"
// clause between the role name and SET/RESET. Returns ok=false for any
// other SQL (including the attribute/RENAME forms tryHandleRoleDDL handles
// elsewhere), leaving the caller to fall through to its existing behaviour.
//
// "ALTER ROLE ALL SET ..." (PG grammar: `ALTER ROLE ALL opt_in_database
// SetResetClause`, gram.y ~line 1377 — a distinct production from the
// RoleSpec-carrying form, not `RoleSpec: ALL`; AlterRoleSet, user.c ~line
// 1000, leaves `roleid = InvalidOid` (0) when `stmt->role == NULL`) is
// recognised below as op.allRoles when the bare, UNQUOTED keyword ALL
// follows ALTER ROLE/USER — a quoted `"ALL"`/`"all"` names a real role and
// must still resolve via RoleOID, exactly like real PG's grammar only
// matches the bare ALL keyword token.
func parseAlterRoleConfig(sql string) (alterRoleConfigOp, bool) {
	s := strings.TrimSpace(sql)
	for strings.HasSuffix(s, ";") {
		s = strings.TrimSpace(strings.TrimSuffix(s, ";"))
	}
	lower := strings.ToLower(s)
	var stripped string
	switch {
	case strings.HasPrefix(lower, "alter role "):
		stripped = s[len("alter role "):]
	case strings.HasPrefix(lower, "alter user "):
		stripped = s[len("alter user "):]
	default:
		return alterRoleConfigOp{}, false
	}
	quoted := strings.HasPrefix(strings.TrimLeft(stripped, " \t\r\n"), `"`)
	roleName, rest, ok := splitLeadingSQLToken(stripped)
	if !ok || roleName == "" {
		return alterRoleConfigOp{}, false
	}
	op := alterRoleConfigOp{roleName: roleName}
	if !quoted && strings.EqualFold(roleName, "all") {
		op.allRoles = true
		op.roleName = ""
	}
	lowerRest := strings.ToLower(rest)
	if strings.HasPrefix(lowerRest, "in database ") {
		dbName, r2, ok := splitLeadingSQLToken(rest[len("in database "):])
		if !ok || dbName == "" {
			return alterRoleConfigOp{}, false
		}
		op.hasDatabase = true
		op.dbName = dbName
		rest = r2
		lowerRest = strings.ToLower(rest)
	}
	switch {
	case strings.HasPrefix(lowerRest, "set "):
		rest = strings.TrimSpace(rest[len("set "):])
		if name, value, reset, matched := parseSetRestSpecialForm(rest); matched {
			op.configName = name
			if reset {
				op.reset = true
			} else {
				op.configValue = value
			}
			return op, true
		}
		configName, rest, ok := splitLeadingSQLToken(rest)
		if !ok || configName == "" {
			return alterRoleConfigOp{}, false
		}
		// "var_name FROM CURRENT" — see parseAlterDatabaseConfig's identical
		// branch for the grammar citation; resolved at apply time.
		if strings.EqualFold(strings.TrimSpace(rest), "from current") {
			op.configName = configName
			op.fromCurrent = true
			return op, true
		}
		switch lr := strings.ToLower(rest); {
		case strings.HasPrefix(lr, "to "):
			rest = strings.TrimSpace(rest[len("to "):])
		case strings.HasPrefix(rest, "="):
			rest = strings.TrimSpace(rest[1:])
		default:
			return alterRoleConfigOp{}, false
		}
		if strings.EqualFold(rest, "default") {
			op.configName = configName
			op.reset = true
			return op, true
		}
		value, ok := flattenConfigValueList(rest)
		if !ok {
			return alterRoleConfigOp{}, false
		}
		op.configName = configName
		op.configValue = value
		return op, true
	case strings.HasPrefix(lowerRest, "reset "):
		rest = strings.TrimSpace(rest[len("reset "):])
		if strings.EqualFold(rest, "all") {
			op.resetAll = true
			return op, true
		}
		configName, _, ok := splitLeadingSQLToken(rest)
		if !ok || configName == "" {
			return alterRoleConfigOp{}, false
		}
		op.configName = configName
		op.reset = true
		return op, true
	}
	return alterRoleConfigOp{}, false
}

// applyAlterRoleConfig applies a parsed ALTER ROLE ... SET/RESET operation,
// mirroring applyAlterDatabaseConfig's shape (role DDL has no notice-string
// channel, unlike database DDL's IF EXISTS-drop notice, so this returns
// (handled, err) rather than (handled, notice, err)). Naming any database
// other than the connection's own liveDBName via IN DATABASE is a silent
// no-op — see applyAlterDatabaseConfig's doc comment for why (goopg v0 has
// no GUC-override storage for a database it isn't connected to).
func (s *Server) applyAlterRoleConfig(op alterRoleConfigOp, liveDBName string, resolveCurrent currentGUCResolver) (bool, error) {
	if s.cfg.Catalog == nil {
		return false, nil
	}
	var roleOid uint32
	if !op.allRoles {
		roleName := strings.Trim(op.roleName, `"`)
		var found bool
		roleOid, found = s.cfg.Catalog.RoleOID(roleName)
		if !found {
			return true, roleDoesNotExistErr(roleName)
		}
	}
	// op.allRoles ("ALTER ROLE ALL ...") leaves roleOid at its zero value —
	// real PG's AlterRoleSet does the same (roleid stays InvalidOid/0 when
	// stmt->role == NULL), so it lands in the same setrole=0 row as any
	// other cluster-wide override; see parseAlterRoleConfig's doc comment.
	reg, ok := s.cfg.Catalog.(roleConfigRegistry)
	if !ok {
		return false, nil
	}
	var dbOid uint32
	if op.hasDatabase {
		if !strings.EqualFold(strings.Trim(op.dbName, `"`), liveDBName) {
			return true, nil
		}
		dbOid = catalog.FirstUserOID
	}
	if op.fromCurrent {
		// Resolve the live session's CURRENT effective value now — see
		// applyAlterDatabaseConfig's identical branch for the rationale
		// (only reached once any "IN DATABASE <other>" no-op has already
		// returned above).
		if resolveCurrent == nil {
			return true, &roleError{code: errcodes.UndefinedObject, msg: fmt.Sprintf("unrecognized configuration parameter %q", op.configName)}
		}
		val, ok := resolveCurrent(op.configName)
		if !ok {
			return true, &roleError{code: errcodes.UndefinedObject, msg: fmt.Sprintf("unrecognized configuration parameter %q", op.configName)}
		}
		op.configValue = val
	}
	switch {
	case op.resetAll:
		reg.ResetAllRoleConfig(roleOid, dbOid)
	case op.reset:
		reg.ResetRoleConfig(roleOid, dbOid, op.configName)
	default:
		reg.SetRoleConfig(roleOid, dbOid, op.configName, op.configValue)
	}
	// B4.2: re-sync the (dbOid, roleOid) pg_db_role_setting heap row from the
	// current registry state — replaces RecordKindAlterRoleSetConfig(76)/
	// ResetConfig(77)/ResetAllConfig(78).
	if err := s.syncDbRoleSettingHeap(dbOid, roleOid, reg.RoleConfigEntries(roleOid, dbOid)); err != nil {
		return true, err
	}
	return true, nil
}

// roleRenameFromAlter recognises the `ALTER ROLE/USER <name> RENAME TO
// <newname>` form and extracts both names. ok is false for any other ALTER
// ROLE/USER shape (attribute form, SET/RESET, IN DATABASE).
func roleRenameFromAlter(norm string) (name, newName string, ok bool) {
	var rest string
	switch {
	case strings.HasPrefix(norm, "alter role "):
		rest = strings.TrimSpace(norm[len("alter role "):])
	case strings.HasPrefix(norm, "alter user "):
		rest = strings.TrimSpace(norm[len("alter user "):])
	default:
		return "", "", false
	}
	name = extractFirstSQLIdent(norm, rest)
	if name == "" {
		return "", "", false
	}
	pos := strings.Index(rest, name)
	if pos < 0 {
		return "", "", false
	}
	tail := strings.TrimSpace(rest[pos+len(name):])
	if !strings.HasPrefix(tail, "rename to ") {
		return "", "", false
	}
	tail = strings.TrimSpace(tail[len("rename to "):])
	newName = extractFirstSQLIdent(norm, tail)
	if newName == "" {
		return "", "", false
	}
	return name, newName, true
}

// roleDoesNotExistErr builds PG's 42704-shaped role error text (the
// "does not exist" phrasing routes through roleErrorSQLState below).
func roleDoesNotExistErr(name string) error {
	return &roleError{code: errcodes.UndefinedObject, msg: "role \"" + name + "\" does not exist"}
}

// roleAlreadyExistsErr builds PG's 42710-shaped "role already exists" error
// (RenameRole's "make sure the new name doesn't exist" check, user.c).
func roleAlreadyExistsErr(name string) error {
	return &roleError{code: errcodes.DuplicateObject, msg: "role \"" + name + "\" already exists"}
}

// reservedRoleNameErr builds PG's 42939-shaped "reserved role name" error
// (RenameRole's IsReservedName check, user.c: names starting with "pg_" are
// reserved for the system). PG pairs this errmsg with a fixed errdetail at
// all three call sites (user.c:356,1388,1395), not a per-name one.
func reservedRoleNameErr(name string) error {
	return &roleError{
		code:   errcodes.ReservedName,
		msg:    "role name \"" + name + "\" is reserved",
		detail: "Role names starting with \"pg_\" are reserved.",
	}
}

// checkCreateRolePrivileges ports the CREATEROLE-privilege gate from
// CreateRole (postgres/src/backend/commands/user.c ~313-343, "Check some
// permissions first"): a non-superuser actingRole must itself hold
// CREATEROLE to create ANY role at all, and beyond that may only hand a new
// role SUPERUSER/CREATEDB/REPLICATION/BYPASSRLS if it holds that exact
// attribute itself — SUPERUSER can never be handed out by a non-superuser,
// full stop, matching user.c's unconditional `if (issuper)` (no matching
// "unless you have SUPERUSER yourself" escape hatch, unlike the other
// three). Callers only invoke this when actingRole != "" (the
// connTx.NonSuperuserRole convention for "not the bootstrap superuser");
// "" bypasses the whole gate here, exactly mirroring
// `if (!superuser_arg(currentUserId))` wrapping this entire block upstream.
// An actingRole with no recorded RoleAttrs (LookupRoleAttrs miss) fails
// closed as "no privileges" rather than skipping the check, since every
// live session's effective role is registered in practice by the time DDL
// runs. M0134-0114.
func checkCreateRolePrivileges(im *catalog.InMemory, actingRole string, newAttrs catalog.RoleAttrs) error {
	curAttrs, found := im.LookupRoleAttrs(actingRole)
	if !found {
		curAttrs = catalog.RoleAttrs{}
	}
	if curAttrs.Superuser {
		return nil
	}
	if !curAttrs.CreateRole {
		return &roleError{
			code:   errcodes.InsufficientPrivilege,
			msg:    "permission denied to create role",
			detail: "Only roles with the CREATEROLE attribute may create roles.",
		}
	}
	if newAttrs.Superuser {
		return createOrAlterRoleAttrDeniedErr("create", "SUPERUSER")
	}
	if newAttrs.CreateDB && !curAttrs.CreateDB {
		return createOrAlterRoleAttrDeniedErr("create", "CREATEDB")
	}
	if newAttrs.Replication && !curAttrs.Replication {
		return createOrAlterRoleAttrDeniedErr("create", "REPLICATION")
	}
	if newAttrs.BypassRLS && !curAttrs.BypassRLS {
		return createOrAlterRoleAttrDeniedErr("create", "BYPASSRLS")
	}
	return nil
}

// checkAlterRoleAttrPrivileges ports the simple, non-ownership-scoped half
// of AlterRole's permission gate (user.c ~757-816): touching the SUPERUSER
// attribute at all — SUPERUSER or NOSUPERUSER, regardless of the role's
// CURRENT superuser status — always requires actingRole itself be
// superuser (`!superuser() && dissuper`), and once past that, touching
// CREATEDB/REPLICATION/BYPASSRLS (again: either polarity) requires
// actingRole hold that same attribute itself
// (`dcreatedb && !have_createdb_privilege()` etc).
//
// This deliberately does NOT port the surrounding CREATEROLE+ADMIN-OPTION-
// on-target gate (upstream's `if (!have_createrole_privilege() ||
// !is_admin_of_role(currentUserId, roleid))` block, which additionally
// requires ADMIN OPTION on the specific target role for ANY attribute
// change) — that needs role-ownership/admin-option tracking the text-
// substitution ALTER ROLE path here does not have; ledgered as a follow-up
// (M0134-0114 deferral ledger row). norm is the already-lower-cased,
// trimmed ALTER ROLE statement text; attribute presence is detected the
// same substring way applyRoleAttrOptions itself parses values, so the two
// stay in lockstep by construction.
func checkAlterRoleAttrPrivileges(im *catalog.InMemory, actingRole, norm string) error {
	curAttrs, found := im.LookupRoleAttrs(actingRole)
	if !found {
		curAttrs = catalog.RoleAttrs{}
	}
	if curAttrs.Superuser {
		return nil
	}
	if alterRoleTouchesAttr(norm, "superuser") {
		return createOrAlterRoleAttrDeniedErr("alter", "SUPERUSER")
	}
	if alterRoleTouchesAttr(norm, "createdb") && !curAttrs.CreateDB {
		return createOrAlterRoleAttrDeniedErr("alter", "CREATEDB")
	}
	if alterRoleTouchesAttr(norm, "replication") && !curAttrs.Replication {
		return createOrAlterRoleAttrDeniedErr("alter", "REPLICATION")
	}
	if alterRoleTouchesAttr(norm, "bypassrls") && !curAttrs.BypassRLS {
		return createOrAlterRoleAttrDeniedErr("alter", "BYPASSRLS")
	}
	return nil
}

// alterRoleTouchesAttr reports whether norm's ALTER ROLE attribute list
// mentions attr in either polarity (e.g. "superuser" or "nosuperuser") —
// mirrors applyRoleAttrOptions' own `strings.Contains(norm, " no"+attr)` /
// `strings.Contains(norm, " "+attr)` pair, since PG treats naming an
// attribute at all (any value) as "touched" for permission purposes.
func alterRoleTouchesAttr(norm, attr string) bool {
	return strings.Contains(norm, " "+attr) || strings.Contains(norm, " no"+attr)
}

// createOrAlterRoleAttrDeniedErr builds the shared "Only roles with the X
// attribute may VERB roles/this role with/the X attribute" 42501 errdetail
// user.c emits at every one of these 8 near-identical call sites (4 in
// CreateRole, 4 in AlterRole) — the two verbs/phrasings differ only in
// "create"/"alter" and "roles with"/"change the".
func createOrAlterRoleAttrDeniedErr(verb, attr string) error {
	if verb == "create" {
		return &roleError{
			code:   errcodes.InsufficientPrivilege,
			msg:    "permission denied to create role",
			detail: fmt.Sprintf("Only roles with the %s attribute may create roles with the %s attribute.", attr, attr),
		}
	}
	return &roleError{
		code:   errcodes.InsufficientPrivilege,
		msg:    "permission denied to alter role",
		detail: fmt.Sprintf("Only roles with the %s attribute may change the %s attribute.", attr, attr),
	}
}

type roleError struct {
	code   errcodes.Code
	msg    string
	detail string
}

func (e *roleError) Error() string { return e.msg }

// roleNameFromCreate extracts the role name from a normalised CREATE ROLE/USER statement.
// The input is already lower-cased and trimmed.
func roleNameFromCreate(norm string) string {
	var rest string
	switch {
	case strings.HasPrefix(norm, "create role "):
		rest = strings.TrimSpace(norm[len("create role "):])
	case strings.HasPrefix(norm, "create user "):
		rest = strings.TrimSpace(norm[len("create user "):])
	case strings.HasPrefix(norm, "create group "):
		rest = strings.TrimSpace(norm[len("create group "):])
	default:
		return ""
	}
	// Skip optional WITH keyword.
	if rest == "with" || strings.HasPrefix(rest, "with ") {
		rest = strings.TrimSpace(rest[4:])
	}
	return extractFirstSQLIdent(norm, rest)
}

// roleNameFromDrop extracts (name, ifExists) from a normalised DROP ROLE/USER statement.
func roleNameFromDrop(norm string) (name string, ifExists bool) {
	var rest string
	switch {
	case strings.HasPrefix(norm, "drop role "):
		rest = strings.TrimSpace(norm[len("drop role "):])
	case strings.HasPrefix(norm, "drop user "):
		rest = strings.TrimSpace(norm[len("drop user "):])
	case strings.HasPrefix(norm, "drop group "):
		rest = strings.TrimSpace(norm[len("drop group "):])
	default:
		return "", false
	}
	if strings.HasPrefix(rest, "if exists ") {
		ifExists = true
		rest = strings.TrimSpace(rest[len("if exists "):])
	}
	name = extractFirstSQLIdent(norm, rest)
	return name, ifExists
}

// extractFirstSQLIdent extracts the first SQL identifier (quoted or unquoted)
// from rest.  norm is used only for error context; it is not otherwise needed.
// Returns "" on failure.
func extractFirstSQLIdent(_ string, rest string) string {
	if rest == "" {
		return ""
	}
	// Double-quoted identifier: preserves internal case after lower-casing
	// by normalizeCompatSQL, so "RegresS" becomes "regress".
	if rest[0] == '"' {
		// Find closing quote.
		end := strings.Index(rest[1:], "\"")
		if end < 0 {
			return rest[1:] // unbalanced quote — take the rest
		}
		return rest[1 : end+1]
	}
	// Unquoted identifier: take up to first space/semicolon.
	end := strings.IndexAny(rest, " \t\n\r;,")
	if end < 0 {
		return rest
	}
	return rest[:end]
}

// splitLeadingRoleDDL splits a multi-statement batch whose FIRST statement is a
// CREATE/DROP ROLE/USER/GROUP command — the forms the parser does not yet
// recognise, so the whole batch reaches the parse-failure recovery path. It
// returns (firstStmt, remainder, true) when the leading statement is role DDL
// AND there is at least one more statement after it; otherwise (… , false).
//
// Without this, dispatch's single-statement role-DDL intercept handles the
// WHOLE batch (e.g. "CREATE ROLE x; CREATE TABLE y") as just the CREATE ROLE
// and silently drops the trailing CREATE TABLE — the failure the *-conflict
// isolation specs' setup blocks hit (their setup is one batch). M0118-0008.
func splitLeadingRoleDDL(sql string) (first, rest string, ok bool) {
	end := firstTopLevelSemicolon(sql)
	if end < 0 {
		return "", "", false // single statement; let the normal intercept handle it
	}
	first = sql[:end]
	rest = strings.TrimSpace(sql[end+1:])
	if rest == "" {
		return "", "", false // trailing ';' only — not a real second statement
	}
	norm := normalizeCompatSQL(first)
	switch {
	case strings.HasPrefix(norm, "create role "), strings.HasPrefix(norm, "create user "),
		strings.HasPrefix(norm, "create group "),
		strings.HasPrefix(norm, "drop role "), strings.HasPrefix(norm, "drop user "),
		strings.HasPrefix(norm, "drop group "):
		return first, rest, true
	}
	return "", "", false
}

// firstTopLevelSemicolon returns the byte index of the first ';' that is not
// inside a single-/double-quoted string, a dollar-quoted string, or a comment.
// Returns -1 when there is no such separator.
func firstTopLevelSemicolon(sql string) int {
	i, n := 0, len(sql)
	for i < n {
		c := sql[i]
		switch {
		case c == ';':
			return i
		case c == '\'' || c == '"':
			// Skip a quoted string; doubled quote is an escaped quote.
			q := c
			i++
			for i < n {
				if sql[i] == q {
					if i+1 < n && sql[i+1] == q {
						i += 2
						continue
					}
					i++
					break
				}
				i++
			}
		case c == '-' && i+1 < n && sql[i+1] == '-':
			// Line comment to end of line.
			for i < n && sql[i] != '\n' {
				i++
			}
		case c == '/' && i+1 < n && sql[i+1] == '*':
			// Block comment (PostgreSQL block comments do not nest in this
			// simplified scan; role/DDL setup never relies on nesting).
			i += 2
			for i < n && !(sql[i] == '*' && i+1 < n && sql[i+1] == '/') {
				i++
			}
			i += 2
		case c == '$':
			// Dollar-quoted string: $tag$ … $tag$.
			if tag, after, isDollar := scanDollarTag(sql, i); isDollar {
				if rel := strings.Index(sql[after:], tag); rel >= 0 {
					i = after + rel + len(tag)
					continue
				}
				return -1 // unterminated dollar quote — no top-level separator
			}
			i++
		default:
			i++
		}
	}
	return -1
}

// scanDollarTag recognises a dollar-quote opening tag ($tag$ or $$) starting at
// sql[i]. On success it returns the full tag text, the index just past it, and
// true. Tags are $ + optional identifier chars + $.
func scanDollarTag(sql string, i int) (tag string, after int, ok bool) {
	n := len(sql)
	if i >= n || sql[i] != '$' {
		return "", 0, false
	}
	j := i + 1
	for j < n {
		ch := sql[j]
		if ch == '$' {
			return sql[i : j+1], j + 1, true
		}
		if ch != '_' && !(ch >= 'a' && ch <= 'z') && !(ch >= 'A' && ch <= 'Z') &&
			!(ch >= '0' && ch <= '9') {
			return "", 0, false
		}
		j++
	}
	return "", 0, false
}

// roleErrorSQLState returns the SQLSTATE for role-related errors.
// PostgreSQL uses 42704 (undefined_object) for "role does not exist".
func roleErrorSQLState(err error) errcodes.Code {
	if re, ok := err.(*roleError); ok && re.code != "" {
		return re.code
	}
	if err != nil && strings.Contains(err.Error(), "does not exist") {
		return errcodes.UndefinedObject
	}
	return errcodes.SystemError
}

// roleErrorDetailFields returns the wire ErrorField(s) carrying a role
// error's errdetail, if any (e.g. reservedRoleNameErr's fixed pg_-prefix
// detail text). Empty for role errors with no PG errdetail counterpart.
func roleErrorDetailFields(err error) []libpq.ErrorField {
	if re, ok := err.(*roleError); ok && re.detail != "" {
		return []libpq.ErrorField{{Code: libpq.FieldDetail, Value: re.detail}}
	}
	return nil
}

// roleErrorDetail returns a role error's bare errdetail text (the
// extended-protocol counterpart of roleErrorDetailFields, whose wire-field
// wrapping is simple-query-only). "" when err carries no PG errdetail.
func roleErrorDetail(err error) string {
	if re, ok := err.(*roleError); ok {
		return re.detail
	}
	return ""
}
