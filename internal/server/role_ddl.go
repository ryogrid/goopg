package server

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
	"strings"

	"github.com/goopg/goopg/internal/auth"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/protocol"
	"github.com/goopg/goopg/internal/sqlstate"
	"github.com/goopg/goopg/internal/wal"
)

// tryHandleRoleDDL returns (handled, err). dbName is the calling
// connection's own database (connTx.DBName) — needed only for the ALTER
// ROLE ... [IN DATABASE ...] SET/RESET branch, mirroring
// tryHandleDatabaseDDL's liveDBName parameter.
//   - handled=true, err=nil:   statement was handled successfully
//   - handled=true, err!=nil:  statement was handled but failed (e.g. role not found)
//   - handled=false, err=nil:  not a role DDL statement; caller should continue
func (s *Server) tryHandleRoleDDL(sql string, dbName string, resolveCurrent currentGUCResolver) (bool, error) {
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
		attrs := catalog.RoleAttrs{CanLogin: strings.HasPrefix(norm, "create user ")}
		applyRoleAttrOptions(sql, norm, &attrs)
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
		attrs := catalog.RoleAttrs{CanLogin: isBootstrap, Superuser: isBootstrap}
		im, isInMem := s.cfg.Catalog.(*catalog.InMemory)
		if isInMem {
			if cur, found := im.LookupRoleAttrs(name); found {
				attrs = cur
			}
		}
		applyRoleAttrOptions(sql, norm, &attrs)
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
		if err := s.unregisterRole(name, ifExists); err != nil {
			return true, err
		}
		// Also unregister from catalog.
		if s.cfg.Catalog != nil {
			s.cfg.Catalog.UnregisterRole(name)
		}
		s.removeRoleCredential(name)
		if s.cfg.WAL != nil {
			if _, _, werr := s.cfg.WAL.Append(wal.EncodeDropRole(strings.ToLower(name))); werr != nil {
				return true, werr
			}
		}
		s.syncAuthidHeap()
		return true, nil
	}
	return false, nil
}

// persistRoleState makes a role mutation durable: WAL record (crash tail) +
// pg_authid heap rewrite (durable base). See the file header for the model.
func (s *Server) persistRoleState(name string, attrs catalog.RoleAttrs) error {
	if s.cfg.WAL != nil {
		var oid uint32
		if im, ok := s.cfg.Catalog.(*catalog.InMemory); ok {
			oid, _ = im.RoleOID(name)
		}
		if _, _, werr := s.cfg.WAL.Append(wal.EncodeRoleState(wal.RoleStatePayload{
			Name:      strings.ToLower(name),
			OID:       oid,
			CanLogin:  attrs.CanLogin,
			Superuser: attrs.Superuser,
			CredType:  attrs.CredType,
			Secret:    attrs.Secret,
		})); werr != nil {
			return werr
		}
	}
	s.syncAuthidHeap()
	return nil
}

// syncAuthidHeap rebuilds the pg_authid heap file from the catalog role
// registry. Best-effort: a failure is logged but does not fail the DDL —
// the WAL record already carries the state until the next successful sync.
func (s *Server) syncAuthidHeap() {
	im, ok := s.cfg.Catalog.(*catalog.InMemory)
	if !ok || s.cfg.DataDir == "" {
		return
	}
	if err := executor.SyncPgAuthidFile(s.cfg.DataDir, im); err != nil && s.cfg.Logger != nil {
		s.cfg.Logger.Warn("pg_authid heap sync failed", "err", err)
	}
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
		return &roleError{code: sqlstate.FeatureNotSupported, msg: "cannot rename the bootstrap superuser"}
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
	if s.cfg.WAL != nil {
		if _, _, werr := s.cfg.WAL.Append(wal.EncodeAlterRoleRename(strings.ToLower(name), strings.ToLower(newName))); werr != nil {
			return werr
		}
	}
	s.syncAuthidHeap()
	return nil
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
// the password literal must be taken from it because normalizeCompatSQL
// lower-cases the whole line and would corrupt a case-sensitive password.
// Unrecognised options (CREATEDB, REPLICATION, VALID UNTIL, ...) are ignored,
// matching the handler's historical accept-and-ignore behaviour.
func applyRoleAttrOptions(sql, norm string, attrs *catalog.RoleAttrs) {
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
			sec, err := auth.NewSCRAMSecret(pw)
			if err != nil {
				return
			}
			attrs.CredType = 3
			attrs.Secret = sec.String()
		}
	}
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
			return true, &roleError{code: sqlstate.UndefinedObject, msg: fmt.Sprintf("unrecognized configuration parameter %q", op.configName)}
		}
		val, ok := resolveCurrent(op.configName)
		if !ok {
			return true, &roleError{code: sqlstate.UndefinedObject, msg: fmt.Sprintf("unrecognized configuration parameter %q", op.configName)}
		}
		op.configValue = val
	}
	switch {
	case op.resetAll:
		reg.ResetAllRoleConfig(roleOid, dbOid)
		if s.cfg.WAL != nil {
			if _, _, werr := s.cfg.WAL.Append(wal.EncodeAlterRoleResetAllConfig(roleOid, dbOid)); werr != nil {
				return true, werr
			}
		}
	case op.reset:
		reg.ResetRoleConfig(roleOid, dbOid, op.configName)
		if s.cfg.WAL != nil {
			if _, _, werr := s.cfg.WAL.Append(wal.EncodeAlterRoleResetConfig(roleOid, dbOid, op.configName)); werr != nil {
				return true, werr
			}
		}
	default:
		reg.SetRoleConfig(roleOid, dbOid, op.configName, op.configValue)
		if s.cfg.WAL != nil {
			if _, _, werr := s.cfg.WAL.Append(wal.EncodeAlterRoleSetConfig(roleOid, dbOid, op.configName, op.configValue)); werr != nil {
				return true, werr
			}
		}
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
	return &roleError{code: sqlstate.UndefinedObject, msg: "role \"" + name + "\" does not exist"}
}

// roleAlreadyExistsErr builds PG's 42710-shaped "role already exists" error
// (RenameRole's "make sure the new name doesn't exist" check, user.c).
func roleAlreadyExistsErr(name string) error {
	return &roleError{code: sqlstate.DuplicateObject, msg: "role \"" + name + "\" already exists"}
}

// reservedRoleNameErr builds PG's 42939-shaped "reserved role name" error
// (RenameRole's IsReservedName check, user.c: names starting with "pg_" are
// reserved for the system). PG pairs this errmsg with a fixed errdetail at
// all three call sites (user.c:356,1388,1395), not a per-name one.
func reservedRoleNameErr(name string) error {
	return &roleError{
		code:   sqlstate.ReservedName,
		msg:    "role name \"" + name + "\" is reserved",
		detail: "Role names starting with \"pg_\" are reserved.",
	}
}

type roleError struct {
	code   sqlstate.Code
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
func roleErrorSQLState(err error) sqlstate.Code {
	if re, ok := err.(*roleError); ok && re.code != "" {
		return re.code
	}
	if err != nil && strings.Contains(err.Error(), "does not exist") {
		return sqlstate.UndefinedObject
	}
	return sqlstate.SystemError
}

// roleErrorDetailFields returns the wire ErrorField(s) carrying a role
// error's errdetail, if any (e.g. reservedRoleNameErr's fixed pg_-prefix
// detail text). Empty for role errors with no PG errdetail counterpart.
func roleErrorDetailFields(err error) []protocol.ErrorField {
	if re, ok := err.(*roleError); ok && re.detail != "" {
		return []protocol.ErrorField{{Code: protocol.FieldDetail, Value: re.detail}}
	}
	return nil
}
