// Package server implements the goopg listener and per-connection lifecycle.
//
// # Per-connection goroutine model (M0042-0004)
//
// goopg spawns one goroutine per client TCP connection (serveConn). That
// goroutine is the per-backend analogue of PostgreSQL's per-backend process
// and owns exactly what the upstream backend owns:
//
//   - The active transaction state (mvcc.Transaction / mvcc.Snapshot).
//   - The pinned-buffer set for the transaction's lifetime (storage.Pool.Pin/Unpin).
//   - All WAL insert calls (wal.Writer.Append — runs on the client goroutine).
//   - The synchronous-commit WAL flush (wal.Writer.FlushUpTo after commit).
//
// The goroutine does NOT own:
//   - Background page flushing — that is Pool.FlushAll / Pool.FlushAllPaced,
//     which are checkpointer-only (see internal/wal/checkpointer.go).
//   - The WAL writer drain cycle — that is the walwriterLoop goroutine
//     started by initdb.Open (M0042-0003).
//   - Replication sender cycles — those are independent walsender goroutines.
//   - Checkpointer / autovacuum / WAL retention — all independent goroutines.
//
// This boundary matches PostgreSQL's "only the backend process may pin
// buffers and insert WAL; background processes flush/sync" model.
// Assertions for the FlushAll boundary are wired via Pool.OnFlushAll.
// See docs/design/0042-0004-client-backend-goroutine-alignment.md.
//
// Design: docs/design/0002-wire-protocol.md.
package postmaster

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/goopg/goopg/internal/utils/activity"
	"github.com/goopg/goopg/internal/libpq/auth"
	"github.com/goopg/goopg/internal/backup"
	"github.com/goopg/goopg/internal/replication"
	"github.com/goopg/goopg/internal/postmaster/autovacuum"
	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/utils/misc"
	"github.com/goopg/goopg/internal/access/transam/control"
	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/port/gls"
	"github.com/goopg/goopg/internal/storage/lmgr"
	"github.com/goopg/goopg/internal/utils/mmgr"
	"github.com/goopg/goopg/internal/access/transam/multixact"
	"github.com/goopg/goopg/internal/access/transam"
	"github.com/goopg/goopg/internal/storage/file"
	"github.com/goopg/goopg/internal/libpq"
	"github.com/goopg/goopg/internal/utils/errcodes"
	"github.com/goopg/goopg/internal/storage"
	"github.com/goopg/goopg/internal/access/transam/xlog"
)

// Config controls a single Server instance.
type Config struct {
	// Address is the listen address in "host:port" form. Default
	// "127.0.0.1:5432" if empty.
	Address string

	// ServerVersion is the value reported in the `server_version`
	// ParameterStatus message. Tracked in
	// docs/design/0001-architecture-overview.md §5.
	ServerVersion string

	// Logger receives connection-lifecycle events. nil means
	// slog.Default().
	Logger *slog.Logger

	// LogStatement controls per-statement query logging, mirroring
	// PostgreSQL's log_statement GUC: "none" (default), "ddl", "mod",
	// or "all". Every received statement whose kind matches the level is
	// emitted to Logger before execution (successful statements too), with
	// the SQL text and, when inside an explicit transaction, the xid — so a
	// verification run can capture exactly which queries a client issued.
	// Driven by the GOOPG_LOG_STATEMENT environment variable (cmd/goopg).
	// An empty or unrecognised value is treated as "none". See
	// docs/design/root-0023-statement-query-logging.md.
	LogStatement string

	// AcceptDeadline bounds Accept() so the goroutine can notice context
	// cancellation. Defaults to 250ms; tests can shrink it.
	AcceptDeadline time.Duration

	// HandshakeTimeout caps how long a client may take to complete the
	// startup handshake. Defaults to 30s.
	HandshakeTimeout time.Duration

	// ShutdownDeadline caps how long Run() waits for in-flight connections
	// to drain after the accept loop exits. Defaults to 120s (graceful
	// shutdown). A zero value means "wait forever" (backward-compatible
	// with tests that embed a server in-process). The STOPIMMEDIATE
	// control-plane command bypasses this deadline entirely (0s wait),
	// mirroring upstream's immediate (SIGQUIT) shutdown.
	// See docs/design/root-0037-nightly-server-shutdown-ladder.md.
	ShutdownDeadline time.Duration

	// Policy decides whether to admit a connection based on the
	// (conn-type, remote, database, user) tuple from the StartupMessage.
	// nil means auth.DefaultPolicy() — loopback-only trust. See
	// docs/design/0003-authentication.md.
	Policy auth.Policy

	// UserStore answers password/md5/scram credential lookups during
	// the auth exchange. nil means "no users configured", which makes
	// every method other than trust/reject fail with
	// ErrMethodUnsupported. The default policy is trust-only, so a nil
	// UserStore is fine for the common loopback-development setup.
	UserStore auth.UserStore

	// Registry is the GUC registry. nil means
	// config.BuildDefaultRegistry() — the seeded set of variables we
	// already advertise. See docs/design/0004-configuration-and-guc.md.
	Registry *misc.Registry

	// ConfigPath is the postgresql.conf path Registry was originally
	// loaded from (cmd/goopg's -config / auto-discovered
	// <DataDir>/postgresql.conf). RELOAD (`goopg reload` or SIGHUP)
	// re-parses this file and re-applies it to Registry. Empty means
	// there is no file to re-read, so RELOAD is a no-op.
	ConfigPath string

	// Catalog / Pool / TxnMgr are the storage handles the executor
	// needs for table-form COPY (and, in future loops, every other
	// table-touching statement). All three must be non-nil to enable
	// the parser→planner→executor path; if any is nil, the wire
	// layer falls back to the v0 string-matching COPY shape so
	// existing deployments without storage configured keep working.
	Catalog catalog.Catalog
	Pool    *storage.Pool
	TxnMgr  *transam.Manager

	// MultiXact is the process-shared MultiXact member store (M0118-0003).
	// The dispatch path plumbs it into every executor.Context so the
	// row-locking path can combine concurrent lock holders into a MultiXactId
	// and resolve a tuple's MultiXactId xmax back to its members. nil disables
	// the multixact path — single-holder xmax behaviour is preserved.
	MultiXact *multixact.Store

	// FSM is the in-memory free-space map (M0046-0003). When non-nil,
	// INSERT consults it to reuse pages freed by VACUUM instead of
	// extending the relation. nil disables the optimisation.
	FSM *storage.FSM

	// VM is the in-memory visibility map (M0046-0004). When non-nil,
	// index-only scans check it to skip heap fetches for ALL_VISIBLE
	// pages. VACUUM sets the bits. nil disables the optimisation.
	VM *storage.VisibilityMap

	// LockMgr, when set, is plumbed into every executor.Context
	// the dispatch path constructs. Operators consult it for
	// relation-level lock acquisition; deadlock detection is
	// handled inside lockmgr (M0012-0002). nil disables locking
	// entirely so existing test setups that don't configure a
	// lock manager keep working unchanged. See
	// docs/design/0012-0003-lock-wait-integration-and-test-matrix.md.
	LockMgr *lmgr.LockManager

	// PubSub is the M0008 logical-replication publication /
	// subscription registry. CREATE PUBLICATION / CREATE
	// SUBSCRIPTION etc. mutate it through the executor; the
	// pg_publication / pg_subscription / pg_publication_rel /
	// pg_publication_tables / pg_subscription_rel virtual
	// catalog views render its current state. nil disables the
	// SQL surface (DDL falls through to feature_not_supported).
	// See docs/design/0008-0003-publication-subscription-ddl.md.
	PubSub *catalog.PubSub

	// Checkpointer, when set, is invoked by the SQL `CHECKPOINT`
	// verb. Production wiring points at *wal.Checkpointer; tests can
	// inject a fake. nil makes CHECKPOINT fail with
	// feature_not_supported, matching the v0 in-process default
	// where the server runs without a WAL writer.
	Checkpointer executor.Checkpointer

	// Promote, when set, is invoked by the control-plane PROMOTE
	// command. The handler drains pending WAL replay, removes
	// `<DataDir>/standby.signal`, and switches the runtime out of
	// standby mode. Wired by `cmd/goopg start` only when the data
	// directory had standby.signal at boot. nil makes PROMOTE
	// reply with "promote not configured" — protecting an
	// already-primary process from a stray `goopg promote`.
	Promote func() error

	// IsStandby, when non-nil, returns true if the server is currently
	// acting as a hot standby. pg_is_in_recovery() calls this on every
	// query. nil makes pg_is_in_recovery() return false (primary mode).
	// M0106-0010 batched-34.
	IsStandby func() bool

	// OnStopImmediate, when set, is invoked by the control-plane
	// STOPIMMEDIATE command (`goopg stop -mode immediate`) BEFORE the
	// run context is cancelled. It must mark the runtime so the final
	// teardown skips its shutdown checkpoint, leaving pg_control at
	// DB_IN_PRODUCTION (an unclean cluster that needs recovery) —
	// mirroring upstream's immediate (SIGQUIT) shutdown. Unlike the
	// graceful STOP path, no CheckpointNow runs here either. nil makes
	// STOPIMMEDIATE behave like a graceful STOP. (M0110-0004 / RW-002 b.)
	OnStopImmediate func() error

	// AutovacuumLauncher, when set, is started as a background
	// goroutine during Run. nil disables autovacuum.
	AutovacuumLauncher *autovacuum.Launcher

	// Activity is the backend-activity registry for
	// pg_catalog.pg_stat_activity. nil disables tracking.
	Activity *activity.Registry

	// Slots, when set, exposes the replication-slot registry to
	// the wire-layer replication-command handler (IDENTIFY_SYSTEM,
	// CREATE_REPLICATION_SLOT, DROP_REPLICATION_SLOT). nil disables
	// replication entirely — replication-mode connections still
	// authenticate but every replication command returns
	// feature_not_supported. See
	// docs/design/0005-0001-streaming-replication-architecture.md.
	Slots *xlog.Slots

	// SyncRep is the synchronous-replication wait primitive
	// (M0102-0005). When non-nil the commit path may call
	// SyncRep.WaitForLSN(commitLSN, mode) after local flush, and the
	// walsender feedback handler calls SyncRep.UpdateStandbyProgress
	// for every Standby Status Update it receives. nil disables sync
	// replication entirely; goopg in async mode is upstream's default.
	SyncRep *xlog.SyncRep

	// WalSenders, when set, lets each walsender goroutine register
	// itself so the pg_stat_replication virtual view can render a
	// live row per active sender. nil makes registration a no-op.
	WalSenders *xlog.Senders

	// WAL exposes the WAL writer's WrittenLSN() so IDENTIFY_SYSTEM
	// can report a current xlogpos. nil → IDENTIFY_SYSTEM reports
	// xlogpos=0/0 (acceptable for tests that don't care about the
	// value).
	WAL *xlog.Writer

	// WALDirPath is the on-disk directory the WAL writer is using
	// (typically `<DataDir>/pg_wal`). Required for the walsender
	// path so RecordIterator can open segments. Empty disables the
	// walsender — START_REPLICATION returns feature_not_supported.
	WALDirPath string

	// WALSegmentSize matches the SegmentSize the WAL writer was
	// constructed with. Zero falls back to wal.DefaultSegmentSize.
	WALSegmentSize int64

	// SystemID is the cluster's pg_control identifier reported by
	// IDENTIFY_SYSTEM. Empty makes IDENTIFY_SYSTEM emit a fixed
	// placeholder. Production wiring derives this from
	// `<DataDir>/global/pg_control` once that file exists.
	SystemID string

	// Timeline is the cluster's current TLI, read from pg_control at
	// startup (M0130-S8). Reported by IDENTIFY_SYSTEM and used to
	// validate START_REPLICATION TIMELINE n. Zero means "use TLI=1"
	// — the default for a freshly-initialised cluster.
	Timeline uint32

	// DataDir, when set, controls where the server writes its
	// `postmaster.pid` file and binds its operator-facing control
	// socket. Empty disables both — useful for in-process tests
	// that don't want a sticky pidfile in /tmp.
	DataDir string

	// MaxQueryPayloadBytes, when non-zero, overrides MaxRegularMessageLength
	// as the per-connection payload ceiling for regular (post-startup) frames.
	// Tests set this to a small value to exercise the oversized-frame path
	// without sending multi-MiB messages. Zero means use MaxRegularMessageLength.
	MaxQueryPayloadBytes int
}

// hasStorage reports whether all three storage handles are configured.
func (c *Config) hasStorage() bool {
	return c.Catalog != nil && c.Pool != nil && c.TxnMgr != nil
}

func (c *Config) defaults() {
	if c.Address == "" {
		c.Address = "127.0.0.1:5432"
	}
	if c.ServerVersion == "" {
		c.ServerVersion = "18.3"
	}
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.AcceptDeadline == 0 {
		c.AcceptDeadline = 250 * time.Millisecond
	}
	if c.HandshakeTimeout == 0 {
		c.HandshakeTimeout = 30 * time.Second
	}
	if c.ShutdownDeadline == 0 {
		c.ShutdownDeadline = 120 * time.Second
	}
	if c.Policy == nil {
		c.Policy = auth.DefaultPolicy()
	}
	if c.Registry == nil {
		c.Registry = misc.BuildDefaultRegistry()
	}
}

// Server is the goopg TCP listener.
//
// Lifecycle:
//
//	srv, err := server.New(cfg)
//	go func() { _ = srv.Run(ctx) }()
//	...
//	cancel(); srv.Wait() // or rely on Run to return
//
// Once Run returns, the Server cannot be restarted.
type Server struct {
	cfg Config

	listener net.Listener
	ready    chan struct{} // closed once listener is bound
	addr     atomic.Pointer[net.TCPAddr]

	connWG        sync.WaitGroup
	nextPID       atomic.Uint32
	nextBackendID atomic.Uint32
	closeOnce     sync.Once

	// shutdownDeadline is set by the control-plane STOP / STOPIMMEDIATE
	// handler before runCancel() fires, then read by Run() to bound the
	// connWG.Wait() after the accept loop exits. See ShutdownDeadline.
	shutdownDeadline time.Duration

	cancelReg *backendCancelRegistry

	// controlListener is the operator-facing command socket; nil
	// when DataDir is unset (in-process tests).
	controlListener *control.Listener
	controlPath     string

	// rolesMu guards the in-memory role set used by CREATE/DROP ROLE.
	// Pre-populated with "postgres" on construction. Roles are tracked
	// in memory only; persistence via pg_auth is deferred.
	rolesMu sync.RWMutex
	roles   map[string]struct{}

	// pc is the cross-session normalized-query plan cache. M0098-0005.
	// nil when the server is in protocol-only mode (no catalog/storage).
	pc *planCache

	// applyLauncher is the logical-replication subscription
	// auto-launcher (M0103-0002). nil when PubSub is unconfigured.
	// Constructed in New, started in Run, drained on Run exit.
	applyLauncher *replication.ApplyLauncher

	// replHandler dispatches MsgQuery frames on a replication=true
	// connection (IDENTIFY_SYSTEM / CREATE_REPLICATION_SLOT /
	// START_REPLICATION / BASE_BACKUP / …). Built once in New because cfg
	// is immutable afterwards; if a future loop makes any of the fields it
	// snapshots runtime-mutable, rebuild it there too. Always non-nil.
	replHandler *replication.Handler

	// notify is the cross-session LISTEN/NOTIFY hub (M0118-0009,
	// async-notify). Always non-nil; channel registrations and queued
	// notifications are keyed by each connection's stable SessionRegistry.
	notify *notifyHub

	// preparedXacts is the process-wide registry of detached prepared
	// transactions, keyed by gid. A RC/RR PREPARE TRANSACTION parks its
	// transaction here (off its originating backend) so a later COMMIT/ROLLBACK
	// PREPARED can finalise it from ANY backend. Always non-nil. M0118-0009
	// (stats — cross-backend two-phase commit).
	preparedXacts *preparedXactStore

	// logStmtLevel is cfg.LogStatement parsed once at construction. It
	// gates the per-statement query log emitted from the simple and
	// extended dispatch paths (statement_log.go). logStmtNone (the
	// default) makes logStatement a cheap no-op.
	logStmtLevel logStatementLevel
}

// New constructs a Server but does not start listening. Use Run to start.
func New(cfg Config) *Server {
	cfg.defaults()
	lvl, ok := parseLogStatementLevel(cfg.LogStatement)
	s := &Server{
		cfg:           cfg,
		ready:         make(chan struct{}),
		cancelReg:     newCancelRegistry(),
		roles:         map[string]struct{}{"postgres": {}},
		notify:        newNotifyHub(),
		preparedXacts: newPreparedXactStore(),
		logStmtLevel:  lvl,
	}
	if !ok {
		cfg.Logger.Warn("unrecognised GOOPG_LOG_STATEMENT value; statement logging disabled",
			"value", cfg.LogStatement, "expected", "none|ddl|mod|all")
	} else if lvl != logStmtNone {
		cfg.Logger.Info("statement logging enabled", "log_statement", strings.ToLower(strings.TrimSpace(cfg.LogStatement)))
	}
	// Seed the connection-time role set from the catalog role registry, which
	// initdb.Open restored from the pg_authid heap + role WAL records
	// (root-0021) — so roles created before a restart keep authenticating.
	if im, ok := cfg.Catalog.(*catalog.InMemory); ok && im != nil {
		for _, rs := range im.AllRoleStates() {
			s.roles[rs.Name] = struct{}{}
		}
	}
	s.nextPID.Store(0)
	// Initialize plan cache when storage handles are present (M0098-0005).
	if cfg.hasStorage() {
		s.pc = newPlanCache()
	}
	// M0100-0006b: wire spec-insert registry cleanup on transaction end.
	if cfg.TxnMgr != nil {
		cfg.TxnMgr.SetOnTxnEnd(executor.DeregisterSpecXID)
	}
	// M0100-0006b: propagate SET application_name to the activity registry
	// so pg_locks JOIN with pg_stat_activity reflects the updated name.
	if cfg.Registry != nil {
		cfg.Registry.OnChange("application_name", func(effVal string) {
			reg, procNum, ok := activity.LookupCurrentGoroutine()
			if !ok {
				return
			}
			reg.UpdateApplicationName(procNum, effVal)
		})
		// M0122-0003 runtime-SET follow-up: propagate SET track_io_timing
		// to the calling backend's per-session flag so the storage-layer
		// I/O wait-event hooks (wired unconditionally in initdb.Open) pick
		// up the change without a server restart.
		cfg.Registry.OnChange("track_io_timing", func(effVal string) {
			reg, procNum, ok := activity.LookupCurrentGoroutine()
			if !ok {
				return
			}
			reg.UpdateTrackIOTiming(procNum, effVal == "on")
		})
		// M0122-0003 writeback follow-up: propagate SET backend_flush_after
		// to the calling backend's per-session threshold (upstream's GUC is
		// PGC_USERSET) so storage.Pool.accountBackendWrite's
		// BackendFlushAfterOverride hook picks up the change without a
		// server restart.
		cfg.Registry.OnChange("backend_flush_after", func(effVal string) {
			reg, procNum, ok := activity.LookupCurrentGoroutine()
			if !ok {
				return
			}
			n, err := strconv.Atoi(effVal)
			if err != nil {
				return
			}
			reg.UpdateBackendFlushAfter(procNum, int32(n))
		})
	}

	// Build the apply-worker launcher when logical replication is
	// wired (PubSub + storage handles). Server.Run starts it so its
	// lifetime matches the listener's. M0103-0002.
	if cfg.PubSub != nil && cfg.hasStorage() {
		s.applyLauncher = replication.NewApplyLauncher(replication.ApplyLauncherConfig{
			PubSub:  cfg.PubSub,
			Catalog: cfg.Catalog,
			Pool:    cfg.Pool,
			TxnMgr:  cfg.TxnMgr,
			Slots:   cfg.Slots,
			Logger:  cfg.Logger,
		})
	}

	// Build the replication-command dispatcher and the BASE_BACKUP handler
	// it fans out to. s.writeQueryError is handed over as a callback so the
	// errQueryErrorSent sentinel — which runPostStartupLoop below matches
	// with errors.Is — never has to leave this package. See
	// replication.WriteQueryErrorFunc.
	s.replHandler = replication.NewHandler(
		replication.Config{
			Logger:         cfg.Logger,
			Catalog:        cfg.Catalog,
			PubSub:         cfg.PubSub,
			Slots:          cfg.Slots,
			SyncRep:        cfg.SyncRep,
			WalSenders:     cfg.WalSenders,
			WAL:            cfg.WAL,
			WALDirPath:     cfg.WALDirPath,
			WALSegmentSize: cfg.WALSegmentSize,
			SystemID:       cfg.SystemID,
			Timeline:       cfg.Timeline,
		},
		s.writeQueryError,
		backup.NewHandler(backup.Config{
			DataDir:        cfg.DataDir,
			WAL:            cfg.WAL,
			WALSegmentSize: cfg.WALSegmentSize,
			Checkpointer:   cfg.Checkpointer,
		}, s.writeQueryError),
	)
	return s
}

// ApplyLauncher exposes the logical-replication subscription
// auto-launcher attached to this Server. Returns nil when PubSub or
// storage is unconfigured. Test-only accessor; production code paths
// use the wake hook plumbed through executor.Context.
func (s *Server) ApplyLauncher() *replication.ApplyLauncher { return s.applyLauncher }

// Addr returns the listen address (resolved port if the config used :0).
// Returns nil before Run has bound the listener; callers in tests should
// wait on Ready() first.
func (s *Server) Addr() *net.TCPAddr { return s.addr.Load() }

// Ready returns a channel that closes once the listener is accepting
// connections. Useful in tests to avoid a race between server startup and
// client dial.
func (s *Server) Ready() <-chan struct{} { return s.ready }

// Run binds the listener and serves connections until ctx is cancelled or
// registerRole adds a role to the in-memory role set.
// If the role already exists this is a no-op (idempotent).
func (s *Server) registerRole(name string) {
	s.rolesMu.Lock()
	s.roles[name] = struct{}{}
	s.rolesMu.Unlock()
}

// unregisterRole removes a role from the in-memory role set.
// Returns an error if the role does not exist and ifExists is false.
func (s *Server) unregisterRole(name string, ifExists bool) error {
	s.rolesMu.Lock()
	defer s.rolesMu.Unlock()
	if _, ok := s.roles[name]; !ok {
		if ifExists {
			return nil
		}
		return fmt.Errorf("role %q does not exist", name)
	}
	delete(s.roles, name)
	return nil
}

// roleExists reports whether a role is in the in-memory set.
func (s *Server) roleExists(name string) bool {
	s.rolesMu.RLock()
	_, ok := s.roles[name]
	s.rolesMu.RUnlock()
	return ok
}

// allRoles returns a snapshot of all role names in the set.
func (s *Server) allRoles() []string {
	s.rolesMu.RLock()
	defer s.rolesMu.RUnlock()
	out := make([]string, 0, len(s.roles))
	for name := range s.roles {
		out = append(out, name)
	}
	return out
}

// a non-recoverable Accept error occurs. It returns nil on a clean shutdown
// (ctx cancelled), or the underlying error otherwise.
func (s *Server) Run(ctx context.Context) error {
	ln, err := net.Listen("tcp", s.cfg.Address)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.cfg.Address, err)
	}
	s.listener = ln
	if tcpAddr, ok := ln.Addr().(*net.TCPAddr); ok {
		s.addr.Store(tcpAddr)
	}
	// M0127-P3.3: sweep spill files a previous crash left in
	// <datadir>/base/pgsql_tmp before any backend can create new ones. PG
	// does the same in RemovePgTempFiles (storage/file/fd.c), called from
	// startup before connections are accepted: a live backend unlinks its
	// own temp files at statement end, so anything present at startup is by
	// definition a stray. Sweeping BEFORE close(s.ready) keeps a test that
	// connects the instant the server is ready from racing the sweep.
	if s.cfg.DataDir != "" {
		if n, err := file.RemoveStrayFiles(s.cfg.DataDir); err != nil {
			// Non-fatal, exactly as PG logs and continues: a stray temp
			// file wastes space, it does not threaten correctness.
			s.cfg.Logger.Warn("removing stray temp files failed", "err", err)
		} else if n > 0 {
			s.cfg.Logger.Info("removed stray temporary files", "count", n,
				"dir", file.Dir(s.cfg.DataDir))
		}
	}

	close(s.ready)
	s.cfg.Logger.Info("goopg listener bound", "address", ln.Addr().String())

	// Operator control plane: pidfile + Unix-domain command socket.
	// Optional — disabled when DataDir is empty (e.g. tests).
	runCtx, runCancel := context.WithCancel(ctx)
	defer runCancel()
	if s.cfg.DataDir != "" {
		if err := s.startControlPlane(runCtx, runCancel, ln); err != nil {
			return err
		}
		defer s.stopControlPlane()
	}

	// One goroutine watches ctx.Done() and closes the listener so Accept
	// unblocks. We also stamp accept deadlines in the loop as a backstop
	// for platforms where Close doesn't unblock pending Accept (e.g. some
	// kernels/golang versions historically had quirks here).
	go func() {
		<-runCtx.Done()
		s.closeOnce.Do(func() { _ = ln.Close() })
	}()

	// Start the apply-worker auto-launcher if logical replication
	// is wired. M0103-0002. Lifetime is bound to runCtx so a STOP
	// from the control plane drains every in-flight apply worker.
	if s.applyLauncher != nil {
		go s.applyLauncher.Run(runCtx)
	}

	// Start the autovacuum launcher if configured.
	if s.cfg.AutovacuumLauncher != nil {
		s.cfg.AutovacuumLauncher.SetLogger(s.cfg.Logger)
		go func() {
			if err := s.cfg.AutovacuumLauncher.Run(runCtx); err != nil &&
				!errors.Is(err, context.Canceled) {
				s.cfg.Logger.Error("autovacuum launcher error", "err", err)
			}
		}()
	}

	acceptErr := s.acceptLoop(runCtx, ln)

	// Wait for in-flight connections to drain, bounded by the
	// deadline the control-plane STOP handler set. A zero deadline
	// (backward-compat for embedded/test servers, or STOPIMMEDIATE)
	// means "wait forever" — the prior behaviour.
	if s.shutdownDeadline > 0 {
		done := make(chan struct{})
		go func() {
			s.connWG.Wait()
			close(done)
		}()
		select {
		case <-done:
			// All backends drained within the deadline.
		case <-time.After(s.shutdownDeadline):
			s.cfg.Logger.Warn(
				"shutdown deadline exceeded; forcing exit",
				"deadline", s.shutdownDeadline,
			)
			// Dump all goroutine stacks for post-mortem analysis
			// so the next loop can identify the blocking site.
			buf := make([]byte, 1<<20) // 1 MiB
			n := runtime.Stack(buf, true)
			// Truncation note: if n == len(buf), the dump was
			// clipped; the kernel's /proc/<pid>/stack per-thread
			// remains available as a fallback if the goopg process
			// is still alive.
			if n == len(buf) {
				s.cfg.Logger.Warn("goroutine dump truncated (1 MiB buffer full)")
			}
			s.cfg.Logger.Warn("forced shutdown goroutine dump (see log for stacks)",
				"goroutines", runtime.NumGoroutine(),
				"dump_len", n)
			// Write the full dump to a file on disk so it
			// survives process exit even if the log is buffered.
			dumpPath := filepath.Join(s.cfg.DataDir, "shutdown_goroutines.txt")
			if err := os.WriteFile(dumpPath, buf[:n], 0644); err != nil {
				s.cfg.Logger.Warn("failed to write shutdown goroutine dump",
					"path", dumpPath, "err", err)
			}
		}
	} else {
		s.connWG.Wait()
	}

	s.cfg.Logger.Info("goopg listener stopped")
	if errors.Is(runCtx.Err(), context.Canceled) || errors.Is(runCtx.Err(), context.DeadlineExceeded) {
		return nil
	}
	return acceptErr
}

// startControlPlane writes the pidfile and binds the Unix-domain
// command socket. STOP cancels runCancel so the accept loop drains.
// RELOAD (control-socket command or SIGHUP) re-parses cfg.ConfigPath
// and re-applies it to cfg.Registry via reloadConfig; a true
// restart-the-listener reload (for PGC_POSTMASTER-context changes)
// is still deferred — those are reported as warnings and require an
// actual process restart.
func (s *Server) startControlPlane(runCtx context.Context, runCancel context.CancelFunc, ln net.Listener) error {
	dir := s.cfg.DataDir
	socketPath := filepath.Join(dir, control.SocketName)
	clog := control.PIDFile{
		PID:        os.Getpid(),
		DataDir:    dir,
		StartedAt:  time.Now(),
		ListenAddr: ln.Addr().String(),
		SocketPath: socketPath,
	}
	if err := control.WritePIDFile(dir, clog); err != nil {
		return fmt.Errorf("write pidfile: %w", err)
	}
	cl, err := control.NewListener(socketPath)
	if err != nil {
		_ = control.RemovePIDFile(dir)
		return fmt.Errorf("control listener: %w", err)
	}
	cl.OnStop = func() error {
		s.cfg.Logger.Info("control: stop requested")
		// Parity note (checkpoint-fpi bundle D-4): upstream performs exactly
		// ONE shutdown checkpoint inside ShutdownXLOG
		// (xlog.c:6640–6680, IS_SHUTDOWN|IMMEDIATE). The former extra
		// CheckpointNow here produced two records per graceful stop and is
		// gone; Close's CheckpointShutdown remains the single final
		// checkpoint and stamps pg_control State = DB_SHUTDOWNED
		// synchronously before process exit (M0110-0004 / RW-002), so
		// `goopg stop` durability is unchanged.
		// Set the graceful deadline before cancelling so Run()
		// bounds its connWG.Wait(). A zero ShutdownDeadline
		// (backward-compat for embedded/test servers) means
		// "wait forever" — the prior behaviour.
		s.shutdownDeadline = s.cfg.ShutdownDeadline
		runCancel()
		return nil
	}
	cl.OnStopImmediate = func() error {
		s.cfg.Logger.Info("control: immediate stop requested")
		// Immediate shutdown: deliberately skip BOTH the OnStop
		// CheckpointNow AND (via the runtime flag set by the wired
		// callback) the final Runtime.Close shutdown checkpoint, so
		// pg_control's State stays DB_IN_PRODUCTION. The cluster then
		// looks unclean to pg_resetwal/pg_rewind/pg_controldata and is
		// recovered via WAL replay on the next start. Mirrors upstream's
		// immediate (SIGQUIT) shutdown. (M0110-0004 / RW-002 b.)
		if s.cfg.OnStopImmediate != nil {
			if err := s.cfg.OnStopImmediate(); err != nil {
				s.cfg.Logger.Warn("immediate-stop hook failed", "err", err)
			}
		}
		// Zero deadline: Run() logs and returns immediately without
		// waiting for backends — mirrors upstream's SIGQUIT.
		s.shutdownDeadline = 0
		runCancel()
		return nil
	}
	cl.OnReload = func() error {
		s.cfg.Logger.Info("control: reload requested")
		s.reloadConfig()
		return nil
	}
	cl.OnCheckpoint = func() error {
		if s.cfg.Checkpointer == nil {
			s.cfg.Logger.Info("control: checkpoint requested but no checkpointer configured")
			return errors.New("checkpoint not configured (server has no WAL writer)")
		}
		s.cfg.Logger.Info("control: checkpoint requested")
		return s.cfg.Checkpointer.CheckpointNow()
	}
	if s.cfg.Promote != nil {
		cl.OnPromote = func() error {
			s.cfg.Logger.Info("control: promote requested")
			return s.cfg.Promote()
		}
	}
	s.controlListener = cl
	s.controlPath = socketPath
	go func() {
		if err := cl.Serve(); err != nil {
			s.cfg.Logger.Debug("control listener serve returned", "err", err)
		}
	}()
	// A bare `kill -HUP <pid>` is upstream's other reload trigger
	// besides `pg_ctl reload` (which itself just sends SIGHUP to the
	// postmaster) — wire it to the same reloadConfig path as the
	// control-socket RELOAD command above so both routes agree.
	hupCh := make(chan os.Signal, 1)
	signal.Notify(hupCh, syscall.SIGHUP)
	go func() {
		defer signal.Stop(hupCh)
		for {
			select {
			case <-runCtx.Done():
				return
			case <-hupCh:
				s.cfg.Logger.Info("SIGHUP received, reloading configuration")
				s.reloadConfig()
			}
		}
	}()
	return nil
}

// reloadConfig re-parses cfg.ConfigPath (if set) and re-applies its
// entries to cfg.Registry, logging a summary of what changed and any
// entries that could not be applied. Invoked by the control-socket
// RELOAD command (`goopg reload`) and by SIGHUP — the same two
// triggers upstream `pg_ctl reload` and `kill -HUP <postmaster-pid>`
// offer. A malformed or missing file never crashes the server; it
// only fails to update the running configuration, matching
// ProcessConfigFile's "log and keep the old values" behaviour.
func (s *Server) reloadConfig() {
	if s.cfg.ConfigPath == "" {
		s.cfg.Logger.Info("control: reload requested but no config file was loaded at startup, nothing to do")
		return
	}
	registry := s.cfg.Registry
	if registry == nil {
		s.cfg.Logger.Warn("control: reload requested but no GUC registry is configured")
		return
	}
	entries, err := misc.ParseConfigFile(s.cfg.ConfigPath)
	if err != nil {
		s.cfg.Logger.Error("control: reload failed to parse config file", "path", s.cfg.ConfigPath, "err", err)
		return
	}
	result := registry.ApplyReloadEntries(entries)
	for _, w := range result.Warnings {
		s.cfg.Logger.Warn("control: reload", "issue", w)
	}
	s.cfg.Logger.Info("control: reload complete", "path", s.cfg.ConfigPath, "changed", result.Changed, "warnings", len(result.Warnings))
}

func (s *Server) stopControlPlane() {
	if s.controlListener != nil {
		_ = s.controlListener.Close()
		s.controlListener = nil
	}
	if s.cfg.DataDir != "" {
		_ = control.RemovePIDFile(s.cfg.DataDir)
	}
}

func (s *Server) acceptLoop(ctx context.Context, ln net.Listener) error {
	tcpLn, hasDeadline := ln.(*net.TCPListener)
	for {
		if ctx.Err() != nil {
			return nil
		}
		if hasDeadline {
			_ = tcpLn.SetDeadline(time.Now().Add(s.cfg.AcceptDeadline))
		}
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			return fmt.Errorf("accept: %w", err)
		}
		s.connWG.Add(1)
		go func() {
			defer s.connWG.Done()
			s.serveConn(ctx, conn)
		}()
	}
}

func (s *Server) serveConn(ctx context.Context, raw net.Conn) {
	pid := s.nextPID.Add(1)
	logger := s.cfg.Logger.With(
		"remote", raw.RemoteAddr().String(),
		"pid", pid,
	)
	// M0058-0005: enable TCP keepalive with a 30-second probe so a
	// half-closed peer (client crash, network partition) is detected
	// within ~3 minutes instead of the OS default (~2 hours). Once
	// the OS marks the conn dead, the next Read or Write surfaces an
	// error to the goroutine, which then propagates to the executor
	// loop's ctx.Err() checks and unwinds the query.
	if tcp, ok := raw.(*net.TCPConn); ok {
		_ = tcp.SetKeepAlive(true)
		_ = tcp.SetKeepAlivePeriod(30 * time.Second)
	}
	// Catch any unrecovered panic so every backend exit is logged at ERROR
	// rather than crashing the process without a log entry. This is a
	// defensive observability wrapper — no panics are expected in normal
	// operation. The connection is always closed and the entry unregistered.
	var exitErr any
	defer func() {
		if r := recover(); r != nil {
			buf := make([]byte, 65536)
			n := runtime.Stack(buf, false)
			logger.Error("backend goroutine panic", "panic", r, "stack", string(buf[:n]))
			exitErr = r
		}
		_ = raw.Close()
		if exitErr != nil {
			logger.Info("connection closed", "reason", "panic")
		} else {
			logger.Info("connection closed")
		}
	}()

	connCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Cancel the connection context when the parent ctx is cancelled. Closing
	// the conn nudges the goroutine out of any blocking read.
	stopWatcher := make(chan struct{})
	defer close(stopWatcher)
	go func() {
		select {
		case <-connCtx.Done():
			_ = raw.SetDeadline(time.Now().Add(50 * time.Millisecond))
		case <-stopWatcher:
		}
	}()

	// Bound the handshake.
	_ = raw.SetDeadline(time.Now().Add(s.cfg.HandshakeTimeout))

	var r *libpq.FrameReader
	if s.cfg.MaxQueryPayloadBytes > 0 {
		r = libpq.NewFrameReaderWithLimit(raw, s.cfg.MaxQueryPayloadBytes)
	} else {
		r = libpq.NewFrameReader(raw)
	}
	w := libpq.NewFrameWriter(raw)

	params, err := s.handleStartup(r, w)
	if err != nil {
		logger.Info("startup failed", "err", err)
		return
	}
	// Clear the handshake deadline; idle-timeout policy is a separate concern.
	_ = raw.SetDeadline(time.Time{})

	user := params["user"]
	app := params["application_name"]
	// PostgreSQL streaming replication: the standby's libpq client
	// passes `replication=true` (or `database`, `1`) in the
	// StartupMessage parameter bag to request a replication-mode
	// connection. We tag the per-conn ctx so runPostStartupLoop can
	// route IDENTIFY_SYSTEM / START_REPLICATION etc. through the
	// walsender path instead of the regular SQL dispatcher. See
	// docs/design/0005-0001-streaming-replication-architecture.md.
	isReplication := isReplicationStartupParam(params["replication"])
	if isReplication {
		logger = logger.With("replication", true)
	}
	logger = logger.With("user", user, "application_name", app)

	if !s.checkAuth(raw, r, w, params, logger) {
		return
	}

	// M0110-0003 (AC-002 gap #7b): reject a connection whose role does not exist,
	// mirroring PG's InitializeSessionUserId FATAL 28000 `role "%s" does not
	// exist` (utils/init/miscinit.c). goopg's in-memory role set (seeded with
	// `postgres`, maintained by CREATE/DROP ROLE) together with any UserStore
	// (pg_auth) account form the runtime authority for which roles exist. The
	// trust auth method never consults either store, so without this an unknown
	// role would connect and the backend would exit 0 — pg_amcheck's
	// `--username no_such_user` probe expects the connection to fail. The
	// password/SCRAM paths already reject unknown users inside checkAuth; this
	// closes the trust-auth hole and matches PG, which checks the role after
	// authentication regardless of method. Gated on a real catalog registry
	// (exactly like the database check below) so bare wire-protocol unit tests,
	// which connect as arbitrary trust-auth users against a catalog-less Server,
	// keep their prior behaviour.
	if user != "" && !isReplication {
		if _, isRegistry := s.cfg.Catalog.(databaseRegistry); isRegistry {
			known := s.roleExists(user)
			if !known && s.cfg.UserStore != nil {
				if _, ok := s.cfg.UserStore.Lookup(user); ok {
					known = true
				}
			}
			if !known {
				s.writeFatal(w, errcodes.InvalidAuthorizationSpecification,
					fmt.Sprintf("role %q does not exist", user))
				logger.Info("connection rejected: unknown role", "role", user)
				return
			}
		}
	}

	// M0110-0003 (AC-002 gap #3): reject a connection to a database that does
	// not exist, mirroring PG's InitPostgres post-authentication 3D000
	// `database "%s" does not exist`. goopg's in-memory database registry is the
	// runtime source of truth (the on-disk pg_database is initdb-only). Skip for
	// replication connections — a physical walsender binds no database, matching
	// PG. Guarded for nil / non-registry catalogs exactly like
	// tryHandleDatabaseDDL so embedded/test catalogs keep their prior behaviour.
	if db := params["database"]; db != "" && !isReplication {
		if reg, ok := s.cfg.Catalog.(databaseRegistry); ok && !reg.HasDatabase(db) {
			s.writeFatal(w, errcodes.InvalidCatalogName,
				fmt.Sprintf("database %q does not exist", db))
			logger.Info("connection rejected: unknown database", "database", db)
			return
		}

		// M0119-0006 (AC-002 residual #1): reject a connection to a database
		// marked `datconnlimit = -2` (left partway through DROP DATABASE),
		// mirroring PG's InitPostgres FATAL 55000 "cannot connect to invalid
		// database" (postinit.c). Only the -2 sentinel is enforced here —
		// positive datconnlimit throttling needs a live per-database
		// connection counter and is a separate, untested-here feature (see
		// the matching deferral ledger row).
		if reg, ok := s.cfg.Catalog.(databaseConnLimitRegistry); ok && reg.DatabaseConnLimit(db) == catalog.DatconnlimitInvalidDB {
			s.writeFatal(w, errcodes.ObjectNotInPrerequisiteState,
				fmt.Sprintf("cannot connect to invalid database %q", db))
			logger.Info("connection rejected: invalid database", "database", db)
			return
		}
	}

	logger.Info("connection established")

	// M0107-0001: session-level memory context. Acquired here and released
	// on connection teardown; stmt-level children are managed in dispatch.go.
	sessCtx := mmgr.Acquire(nil, mmgr.KindSession)
	defer sessCtx.Release()

	// Register backend in the pg_stat_activity registry.
	// M0107-0005: compute procNum here so the client-I/O hot-path closures
	// can call WaitEventStart(procNum, ...) atomically instead of acquiring
	// the old Registry.mu on every wire frame.
	pidStr := activity.PID(pid)
	// Connection-lifetime proc slot (replaces the historical pid-modulo
	// assignment that WRAPPED past ConnSlotCount cumulative connections
	// and clobbered live sessions' slots — see mvcc.AcquireConnSlot).
	var procNum int32
	if s.cfg.TxnMgr != nil {
		var slotErr error
		procNum, slotErr = s.cfg.TxnMgr.AcquireConnSlot()
		if slotErr != nil {
			s.cfg.Logger.Warn("connection rejected: proc slots exhausted", "err", slotErr)
			return
		}
		defer s.cfg.TxnMgr.ReleaseConnSlot(procNum)
	} else {
		// Manager-less unit harnesses: the historical modulo assignment is
		// safe there (single short-lived connections, no churn).
		procNum = int32((pid - 1) % uint32(transam.ConnSlotCount))
	}
	reg := s.cfg.Activity
	if reg != nil {
		clientAddr := raw.RemoteAddr().String()
		clientPort := ""
		if taddr, ok := raw.RemoteAddr().(*net.TCPAddr); ok {
			clientAddr = taddr.IP.String()
			clientPort = fmt.Sprintf("%d", taddr.Port)
		}
		// RegisterAt (not Register): procNum here is the SAME TxnMgr conn-slot
		// value used below for WaitEventStart/UpdateState/PIDForProcNum on this
		// connection's hot path. Register's own PID-hash slot is an independent
		// index space — using it here would make every later per-statement
		// UpdateState call land on the wrong slot, freezing pg_stat_activity's
		// state/query columns at their initial Register-time values for the
		// backend's entire lifetime (found via the partition-drop-index-locking
		// isolation spec: s.query always blank, s.state always "active").
		reg.RegisterAt(procNum, &activity.Backend{
			PID:             pidStr,
			DatName:         params["database"],
			UserName:        user,
			ApplicationName: app,
			ClientAddr:      clientAddr,
			ClientPort:      clientPort,
			BackendStart:    time.Now().UTC().Format(time.RFC3339Nano),
			State:           "active",
			BackendType:     "client_backend",
		})
		// Register this goroutine so pool / AIO / spill wait-event hooks
		// can find the correct procNum via activity.LookupCurrentGoroutine.
		// Hot-path client-I/O closures (below) capture procNum directly
		// and do not touch the goroutine map.
		activity.SetCurrentGoroutine(reg, procNum)
		// Stamp the same procNum as a goroutine-local id so the WAL insert
		// path (wal.state.stripeNum) can pick this backend's stripe with a
		// cheap label read instead of runtime.Stack (analysis/perf-optimize2,
		// fix-01). Inherited by any helper goroutines this backend spawns.
		gls.SetBackendID(procNum)
	}
	defer func() {
		if reg != nil {
			activity.ClearCurrentGoroutine()
			reg.Unregister(pidStr)
		}
	}()

	// M0119-0006 (AC-002 residual #2): reject a non-superuser connection once
	// the database's live connection count exceeds a positive `datconnlimit`,
	// mirroring PG's InitPostgres/CheckMyDatabase FATAL 53300 "too many
	// connections for database" (postinit.c). Placed after reg.Register above
	// so CountByDatName's scan already includes this connection's own slot,
	// matching CountDBConnections' self-inclusive count (postinit.c's own
	// comment: "we create our PGPROC before checking for other PGPROCs").
	// Superuser connections are still counted (matching CountDBConnections
	// having no role filter) but skip the reject check itself, exactly like
	// upstream's `!am_superuser` gate.
	if db := params["database"]; db != "" && !isReplication && reg != nil {
		if limReg, ok := s.cfg.Catalog.(databaseConnLimitRegistry); ok {
			if limit := limReg.DatabaseConnLimit(db); limit >= 0 && !isSuperuserRoleName(user) {
				if count := reg.CountByDatName(db); count > limit {
					s.writeFatal(w, errcodes.TooManyConnections,
						fmt.Sprintf("too many connections for database %q", db))
					logger.Info("connection rejected: too many connections",
						"database", db, "limit", limit, "count", count)
					return
				}
			}
		}
	}

	// Wire client-I/O wait-event hooks on the frame reader/writer.
	// M0107-0005: capture reg + procNum (int32); WaitEventStart/End are
	// now O(1) atomic stores with no global mutex (vs Registry.mu.Lock
	// which accounted for ~53% of all mutex delay at c=100 SO).
	r.OnBeforeRead = func() {
		if reg != nil {
			reg.WaitEventStart(procNum, activity.WaitTypeClient, activity.WaitClientRead)
		}
	}
	r.OnAfterRead = func() {
		if reg != nil {
			reg.WaitEventEnd(procNum)
		}
	}
	w.OnBeforeWrite = func() {
		if reg != nil {
			reg.WaitEventStart(procNum, activity.WaitTypeClient, activity.WaitClientWrite)
		}
	}
	w.OnAfterWrite = func() {
		if reg != nil {
			reg.WaitEventEnd(procNum)
		}
	}

	sess := misc.NewSessionRegistry(s.cfg.Registry)
	// Seed this backend's effective track_io_timing flag from the
	// boot-time GUC default (postgresql.conf / registry bootval); the
	// OnChange hook registered in New() keeps it live thereafter on
	// `SET track_io_timing`. M0122-0003 runtime-SET follow-up.
	if reg != nil {
		if _, eff, ok := sess.Get("track_io_timing"); ok {
			reg.UpdateTrackIOTiming(procNum, eff == "on")
		}
		// Seed this backend's effective backend_flush_after threshold from
		// the boot-time GUC default; the OnChange hook registered in New()
		// keeps it live thereafter on `SET backend_flush_after` (M0122-0003
		// writeback follow-up).
		if _, eff, ok := sess.Get("backend_flush_after"); ok {
			if n, err := strconv.Atoi(eff); err == nil {
				reg.UpdateBackendFlushAfter(procNum, int32(n))
			}
		}
	}
	// Echo StartupMessage values for variables clients commonly send.
	// Failures are not fatal — clients send a wide variety of values
	// and we want to keep the connection going.
	if app != "" {
		_ = sess.Set("application_name", app, false)
	}
	if user != "" {
		// session_authorization is FlagReport; use a layer-write so the
		// effective value matches.
		_ = sess.Set("session_authorization", user, false)
	}
	// is_superuser is FlagReport (GUC_REPORT upstream): libpq captures it
	// from the startup ParameterStatus block and pg_dump's is_superuser()
	// (pg_dump.c) reads that captured value verbatim rather than issuing a
	// live SHOW — it never re-queries after connecting, so this one-time
	// startup value is what getSubscriptions()'s superuser gate actually
	// sees. Before this fix the GUC's BootVal ("off") was never overridden
	// per-connection, so pg_dump treated every connection — including the
	// bootstrap "postgres" superuser — as unprivileged and silently skipped
	// dumping ALL subscriptions (DU-002 slice 423).
	if isSuperuserRoleName(user) {
		_ = sess.SetInternal("is_superuser", "on")
	}

	// Apply generic startup-packet GUC settings. These values are the
	// session-start values (PG's PGC_S_CLIENT source) and become the per-session
	// RESET target — use SetStartup, not Set, so a later RESET restores the
	// startup-packet value rather than the compiled default (design
	// 0134-0075-guc-reset-session-start-value). PostgreSQL's
	// ProcessStartupPacket (backend_startup.c ~line 770-790) treats every
	// startup-packet key other than user/database/options/replication/_pq_.*
	// as a GUC name=value pair (port->guc_options), and separately parses the
	// "options" key's PGOPTIONS-style `-c name=value` tokens the same way.
	// This is the actual wire-level mechanism behind PGDATESTYLE/PGTZ/
	// PGOPTIONS (libpq's fe-connect.c EnvironmentOptions table folds
	// PGDATESTYLE/PGTZ into plain "datestyle"/"timezone" startup keys) and
	// libpq connstrings like `options=-c search_path=foo`. goopg previously
	// only echoed application_name/session_authorization/is_superuser from
	// the startup packet and silently dropped every other key, so none of
	// the above ever reached the backend's runtime GUC state.
	for name, val := range params {
		switch name {
		case "user", "database", "application_name", "replication", "options":
			continue
		}
		if strings.HasPrefix(name, "_pq_.") {
			continue
		}
		_ = sess.SetStartup(name, val)
	}
	for name, val := range parsePGOptions(params["options"]) {
		_ = sess.SetStartup(name, val)
	}

	// Now wire the per-session ParameterStatus emitter so subsequent
	// SET application_name = 'X' (etc.) auto-emits.
	sess.SetReportableHook(func(name, value string) {
		if err := w.WriteParameterStatus(name, value); err != nil {
			logger.Debug("ParameterStatus write failed", "name", name, "err", err)
			return
		}
		_ = w.Flush()
	})

	// Store the backend PID in the session so dispatch paths can
	// update pg_stat_activity state.
	_ = sess.Set("goopg.backend_pid", pidStr, false)

	// Generate and register cancel credentials for this connection.
	secret, err := newSecretKey()
	if err != nil {
		logger.Info("secret key generation failed", "err", err)
		return
	}
	cancelEntry := s.cancelReg.register(pid, secret)
	defer s.cancelReg.unregister(pid)
	// Wire the connection-termination function so a peer's pg_terminate_backend(pid)
	// can tear this connection down (cancelling connCtx fires the FATAL path at the
	// top of the serve loop). M0118-0009.
	cancelEntry.setTerminate(cancel)

	if err := s.sendStartupReply(w, sess, pid, secret); err != nil {
		logger.Info("startup reply failed", "err", err)
		return
	}

	s.runPostStartupLoop(connCtx, cancelEntry, raw, r, w, sess, logger, isReplication, app, params["database"], sessCtx, pid, procNum, user)
}

// isReplicationStartupParam interprets the StartupMessage `replication`
// parameter the way upstream PostgreSQL does: case-insensitive `true`
// or `1` enables physical replication mode; `database` enables logical
// replication (deferred in v0; treated as physical for now); empty /
// `false` / `0` / unrecognised values mean "not a replication
// connection". Mirrors postgres/src/backend/replication/walsender.c
// (`got_STOPPING`, `EnableReplicationOriginCmd`).
// isSuperuserRoleName reports whether roleName is the bootstrap
// superuser. goopg has no CREATE ROLE ... SUPERUSER attribute tracking
// (the whole privilege model is the bootstrap "postgres" role vs.
// everything else — see connTxState.NonSuperuserRole and its SET
// ROLE / SET SESSION AUTHORIZATION call sites in query.go, which use
// the same case-insensitive "POSTGRES" special case), so this mirrors
// that convention rather than introducing a separate one.
func isSuperuserRoleName(roleName string) bool {
	return strings.EqualFold(strings.TrimSpace(roleName), "postgres")
}

// parsePGOptions parses a PGOPTIONS-style command-line options string (the
// startup packet's "options" key) into GUC name=value pairs, covering the
// `-c name=value` form (attached, `-cname=value`, or split across two
// whitespace-separated tokens) that PGOPTIONS/libpq's `options=` connstring
// parameter and every real client actually emit. Unlike PostgreSQL's own
// pg_split_opts (postmaster.c), this does not support shell-style quoting —
// no goopg caller needs it, since values containing spaces would require
// quoting the whole PGOPTIONS string anyway.
func parsePGOptions(opts string) map[string]string {
	result := map[string]string{}
	fields := strings.Fields(opts)
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		if !strings.HasPrefix(f, "-c") {
			continue
		}
		rest := f[len("-c"):]
		if rest == "" {
			if i+1 >= len(fields) {
				continue
			}
			i++
			rest = fields[i]
		}
		if eq := strings.IndexByte(rest, '='); eq > 0 {
			result[rest[:eq]] = rest[eq+1:]
		}
	}
	return result
}

func isReplicationStartupParam(v string) bool {
	switch v {
	case "":
		return false
	case "0", "false", "FALSE", "False":
		return false
	}
	return true
}

// checkAuth runs the configured Policy and the corresponding wire
// exchange (Trust → AuthenticationOk; Password / MD5 → AuthRequest +
// PasswordMessage round-trip; Reject / unsupported method → FATAL
// ErrorResponse). It returns true iff the connection should proceed to
// the parameter-status block.
func (s *Server) checkAuth(raw net.Conn, r *libpq.FrameReader, w *libpq.FrameWriter, params map[string]string, logger *slog.Logger) bool {
	req := auth.Request{
		ConnType: connTypeFor(raw),
		Remote:   remoteIP(raw),
		Database: params["database"],
		User:     params["user"],
	}
	if req.Database == "" {
		// PostgreSQL convention: when no database is provided in the
		// StartupMessage, the user name is used as the database name.
		req.Database = req.User
	}
	decision := s.cfg.Policy.Match(req)
	err := auth.Exchange(decision, r, w, s.cfg.UserStore, req.User)
	switch {
	case err == nil:
		return true
	case isAuthRejected(err) || isInvalidPassword(err) || isUserUnknown(err):
		logger.Info("connection rejected",
			"err", err,
			"method", decision.Method.String(),
		)
		s.writeFatal(w, errcodes.InvalidAuthorizationSpecification, err.Error())
		return false
	case isAuthExchangeFailure(err) || errors.Is(err, auth.ErrUnexpectedFrame):
		// Wire-level failure or client misbehaved during the exchange.
		// The connection is already in a bad state — log and close
		// without trying to emit FATAL, matching how upstream's
		// auth.c quits on EOF during a password packet read.
		logger.Info("auth exchange failed", "err", err)
		return false
	default:
		// ErrMethodUnsupported and any future errors land here.
		logger.Info("auth method not supported",
			"err", err,
			"method", decision.Method.String(),
		)
		s.writeFatal(w, errcodes.FeatureNotSupported, err.Error())
		return false
	}
}

func isAuthRejected(err error) bool {
	var rej auth.ErrRejected
	return errors.As(err, &rej)
}

func isInvalidPassword(err error) bool {
	var bad auth.ErrInvalidPassword
	return errors.As(err, &bad)
}

func isUserUnknown(err error) bool {
	var unk auth.ErrUserUnknown
	return errors.As(err, &unk)
}

func isAuthExchangeFailure(err error) bool {
	var x auth.ErrAuthExchange
	return errors.As(err, &x)
}

// connTypeFor reports the auth.ConnType implied by the listener that
// accepted raw. v0 only listens on TCP; ConnLocal arrives once we add a
// Unix-socket listener (deferred). TLS tagging (ConnHostSSL) lands when
// TLS does.
func connTypeFor(raw net.Conn) auth.ConnType {
	if _, ok := raw.LocalAddr().(*net.UnixAddr); ok {
		return auth.ConnLocal
	}
	return auth.ConnHost
}

// remoteIP returns the client's IP, or nil for connections without one.
func remoteIP(raw net.Conn) net.IP {
	if tcp, ok := raw.RemoteAddr().(*net.TCPAddr); ok {
		return tcp.IP
	}
	return nil
}

// handleStartup reads startup packets, transparently rejecting SSL/GSS
// requests with 'N' and looping back to read the actual startup packet. It
// returns the parsed parameter map for a regular protocol-3.0 session, or
// an error for everything else (cancel requests, unsupported protocol
// versions, malformed packets).
func (s *Server) handleStartup(r *libpq.FrameReader, w *libpq.FrameWriter) (map[string]string, error) {
	for range 3 {
		version, payload, err := r.ReadStartupPacket()
		if err != nil {
			return nil, err
		}
		switch version {
		case libpq.NegotiateSSLCode, libpq.NegotiateGSSCode:
			if err := w.WriteRaw([]byte{'N'}); err != nil {
				return nil, fmt.Errorf("reject SSL/GSS: %w", err)
			}
			if err := w.Flush(); err != nil {
				return nil, fmt.Errorf("flush SSL/GSS rejection: %w", err)
			}
			continue
		case libpq.CancelRequestCode:
			// Dispatch cancel to the target backend and close silently.
			// The protocol spec says: after sending CancelRequest the
			// client closes the connection immediately; the server closes
			// its end silently without any reply.
			if len(payload) == 8 {
				targetPID := binary.BigEndian.Uint32(payload[0:4])
				targetSecret := binary.BigEndian.Uint32(payload[4:8])
				s.cancelReg.cancelQuery(targetPID, targetSecret)
			}
			return nil, errors.New("cancel request handled")
		}
		if version != libpq.ProtocolVersion3_0 {
			major, minor := version>>16, version&0xFFFF
			s.writeFatal(w, errcodes.FeatureNotSupported,
				fmt.Sprintf("unsupported frontend protocol %d.%d: server supports 3.0", major, minor))
			return nil, fmt.Errorf("unsupported protocol %d.%d", major, minor)
		}
		params, err := libpq.ParseStartupParameters(payload)
		if err != nil {
			s.writeFatal(w, errcodes.ProtocolViolation, "invalid startup packet")
			return nil, err
		}
		return params, nil
	}
	return nil, errors.New("too many SSL/GSS negotiation attempts")
}

// sendStartupReply emits the post-auth portion of the startup sequence:
// the ParameterStatus block (driven by the GUC registry), BackendKeyData,
// and ReadyForQuery. AuthenticationOk has already been written by
// checkAuth at this point. secret must be the same value registered in
// the cancel registry so that CancelRequest can look up the backend.
func (s *Server) sendStartupReply(w *libpq.FrameWriter, sess *misc.SessionRegistry, pid, secret uint32) error {
	for _, kv := range sess.ReportableVariables() {
		if err := w.WriteParameterStatus(kv.Name, kv.Value); err != nil {
			return err
		}
	}
	if err := w.WriteBackendKeyData(pid, secret); err != nil {
		return err
	}
	if err := w.ReadyForQuery(); err != nil {
		return err
	}
	return w.Flush()
}

// runPostStartupLoop handles every frame after ReadyForQuery. v0 routes
// simple Query messages into handleQuery; Terminate closes the connection
// cleanly; anything else is an "unsupported" ErrorResponse followed by
// another ReadyForQuery so the client can keep going.
// rollbackOpenTxnOnTeardown aborts an explicit transaction that is still open
// when the connection's dispatch loop exits. It mirrors the dispatch
// `planner.TxRollback` path (roll back the TxnMgr transaction, undo
// in-transaction enum DDL, clear per-connection state — any DROP
// FUNCTION/PROCEDURE deferred to COMMIT is discarded by `connTx.End()`'s
// `EndExplicitTransaction`). The critical effect is `TxnMgr.Rollback`, which
// clears the XID from
// the ProcArray and broadcasts `commitCond` — releasing every backend blocked
// in WaitForXID on this transaction's XID. A no-op when no explicit
// transaction is active (auto-commit statements finish their own per-statement
// transaction inline).
func (s *Server) rollbackOpenTxnOnTeardown(connTx *connTxState, logger *slog.Logger) {
	if connTx == nil || !connTx.InExplicit() || s.cfg.TxnMgr == nil {
		return
	}
	_ = s.cfg.TxnMgr.Rollback(connTx.Tx())
	if s.cfg.Catalog != nil {
		undoEnumDDLForRollback(connTx, s.cfg.Catalog, resolveConnDBOid(s.cfg.Catalog, connTx.DBName))
	}
	connTx.End()
	if logger != nil {
		logger.Info("rolled back open transaction on connection teardown")
	}
}

// cleanupSessionTempObjects drops every temporary object owned by sess at
// backend exit, mirroring PostgreSQL's RemoveTempRelations: the session's temp
// tables (and their implicit composite rowtypes), the non-temp routines that
// depend on those rowtypes (the same name-keyed cascade DISCARD TEMP uses, since
// goopg has no OID-level pg_depend graph), and finally the temp namespace
// registration itself (unlike DISCARD TEMP, which keeps the namespace for reuse).
// A no-op for a session that never created a temporary object. The owner token
// matches executor.sessionTempOwner ("s"+UniqueID). M0118-0009.
func (s *Server) cleanupSessionTempObjects(sess *misc.SessionRegistry) {
	if sess == nil || s.cfg.Catalog == nil {
		return
	}
	im, ok := s.cfg.Catalog.(*catalog.InMemory)
	if !ok {
		return
	}
	id := sess.UniqueID()
	if id == 0 {
		return
	}
	owner := "s" + strconv.FormatUint(id, 10)
	// Capture the temp tables' names BEFORE dropping them: a temp table's
	// implicit composite rowtype dies with it and cascades to any routine
	// referencing that rowtype (e.g. the spec's uses_a_temp_type).
	tempTypeNames := im.SessionTempTableNames(owner)
	im.DropSessionTempObjects(owner)
	if len(tempTypeNames) > 0 {
		if rs := im.Routines(); rs != nil {
			rs.DropRoutinesReferencingTypes(tempTypeNames)
		}
	}
	im.DropTempNamespace(owner)
}

func (s *Server) runPostStartupLoop(ctx context.Context, entry *cancelEntry, raw net.Conn, r *libpq.FrameReader, w *libpq.FrameWriter, sess *misc.SessionRegistry, logger *slog.Logger, isReplication bool, appName, dbName string, sessCtx *mmgr.Context, pid uint32, procNum int32, loginUser string) {
	extended := newExtendedState()
	// procNum is the connection-lifetime ProcArray slot acquired by
	// serveConn via mvcc.AcquireConnSlot (M0107-0004; the slot is reused
	// across all transactions on this connection; Begin clears and
	// re-initialises it per transaction, and serveConn releases it at
	// disconnect).
	extended.ProcNum = procNum                                                                   // thread through to executeExtendedQueryViaExecutor
	extended.DBName = dbName                                                                     // scopes pg_extension per database (M0110-0003 gap #7c)
	connTx := &connTxState{SessCtx: sessCtx, ProcNum: procNum, DBName: dbName, AdvisoryID: sess} // per-connection explicit transaction state (M0096-0005); DBName scopes pg_extension (M0110-0003 gap #7c); AdvisoryID = stable advisory-lock owner identity (M0118-0003)
	// LoginUser/SessionUser seed the session_user()/current_user() identity
	// from the same StartupMessage "user" value already written into the
	// session_authorization GUC (server.go, checkAuth caller) so the two
	// cannot drift. LoginUser is immutable; SessionUser moves with SET/RESET
	// SESSION AUTHORIZATION. M0134-0009.
	connTx.LoginUser = loginUser
	connTx.SessionUser = loginUser
	// Stable per-connection lock-manager identity for transaction-scoped LOCK
	// TABLE heavyweight locks (M0118-0003 lock-nowait). Minted once from the
	// same monotonic counter as per-statement BackendIDs so it never collides
	// with one; locks taken under it (on the executor's dedicated tableLockMgr)
	// survive dispatch.go's per-statement ReleaseAll and are dropped in
	// connTxState.End() at COMMIT/ROLLBACK (and on teardown via
	// rollbackOpenTxnOnTeardown).
	connTx.LockBackendID = lmgr.BackendID(s.nextBackendID.Add(1))
	// Every ReadyForQuery on this connection now reports the live transaction
	// status ('I'/'T'/'E') instead of a hard-coded 'I'. libpq surfaces the byte
	// as PQtransactionStatus; pgbench reads it after a failed command to decide
	// whether the failed block still needs a ROLLBACK, and a permanent 'I' made
	// it skip that ROLLBACK and abort on the next BEGIN with 25P02
	// (AI-20260810-011258-006). See connTxState.wireStatus.
	w.TxStatusFn = connTx.wireStatus
	// LISTEN/NOTIFY identity: the backend PID is the source of NOTIFY deliveries
	// and the SessionRegistry is the hub key. On teardown drop every channel
	// registration and undelivered notification for this session, mirroring
	// PostgreSQL freeing the listen state at backend exit. M0118-0009.
	connTx.BackendPID = pid
	connTx.NotifySession = sess
	defer s.notify.RemoveSession(sess)
	// Map this connection's transaction-scoped lock identity to its backend PID
	// so transaction-scoped relation locks (LOCK TABLE / DDL / scan-read ACCESS
	// SHARE) held on the executor's tableLockMgr surface in pg_locks with a PID
	// that joins pg_stat_activity (design 0118-0070). Dropped at teardown.
	executor.RegisterLockBackendPID(connTx.LockBackendID, pid)
	defer executor.UnregisterLockBackendPID(connTx.LockBackendID)
	// On connection teardown, release EVERY advisory lock this backend still
	// holds — session-scoped as well as transaction-scoped. PostgreSQL frees all
	// advisory locks at backend exit; without this a session-scoped
	// pg_advisory_lock() the client never unlocked (or abandoned the connection
	// while holding) would persist for the server-process lifetime and block
	// every future acquirer of the same key. Runs before the open-txn rollback
	// (defers run LIFO) so the xact-scoped release there is a harmless no-op.
	// M0118-0003.
	defer executor.ReleaseAllAdvisoryLocks(sess)
	// On connection teardown, drop this session's temporary objects (temp tables,
	// their implicit composite rowtypes, the non-temp routines that depend on
	// those rowtypes, and finally the temp namespace itself). PostgreSQL performs
	// this at backend exit via RemoveTempRelations, and crucially BEFORE releasing
	// session-level advisory locks — the temp-schema-cleanup spec relies on that
	// ordering: a peer waiting on the same advisory lock only unblocks once the
	// catalog is clean. Registered AFTER the advisory-release defer so LIFO runs
	// it FIRST. M0118-0009 (temp-schema-cleanup process-exit permutation).
	defer s.cleanupSessionTempObjects(sess)
	// On connection teardown (client disconnect, EOF, read error, admin
	// shutdown — every `return` from the loop below), roll back any still-open
	// explicit transaction so its XID is released from the ProcArray and any
	// goroutine blocked in WaitForXID on that XID is woken. Without this, a
	// client that abandons a connection mid-transaction — e.g. pgbench dropping
	// a connection after a client-level abort — leaves its XID in-progress
	// forever, deadlocking every concurrent backend waiting on it via
	// epqWait → WaitForXID (the pgbench TPC-B UPDATE-contention hang).
	defer s.rollbackOpenTxnOnTeardown(connTx, logger)
	prepStmts := newPreparedStatements() // per-connection prepared statements (M0096-0006)
	var copyIn *copyInState
	for {
		if ctx.Err() != nil {
			s.writeFatal(w, errcodes.AdminShutdown, "terminating connection due to administrator command")
			return
		}
		f, err := r.ReadFrame()
		if err != nil {
			if errors.Is(err, libpq.ErrFrameTooLarge) {
				// The oversized payload was already drained by ReadFrame so the
				// stream is re-synchronised. Send a proper error response and
				// keep the session alive so HammerDB / libpq can retry.
				logger.Info("oversized client message rejected", "err", err)
				if werr := s.writeQueryError(w, errcodes.ProtocolViolation, err.Error()); werr != nil && !errors.Is(werr, errQueryErrorSent) {
					logger.Info("write error after oversized message", "err", werr)
					return
				}
				if werr := w.Flush(); werr != nil {
					logger.Info("flush error after oversized message", "err", werr)
					return
				}
				continue
			}
			if !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
				logger.Info("connection read error", "err", err)
			}
			return
		}
		if copyIn != nil {
			if f.Type == libpq.MsgTerminate {
				return
			}
			done, err := s.handleCopyInFrame(w, copyIn, f)
			if err != nil {
				return
			}
			if done {
				copyIn = nil
			}
			if err := w.Flush(); err != nil {
				return
			}
			continue
		}
		if extended.syncRequired && f.Type != libpq.MsgSync && f.Type != libpq.MsgTerminate {
			continue
		}
		switch f.Type {
		case libpq.MsgTerminate:
			return
		case libpq.MsgQuery:
			// Create a per-query cancellable context and register its
			// cancel function so an incoming CancelRequest can fire it.
			queryCtx, queryCancel := context.WithCancel(ctx)
			entry.setQueryCancel(queryCancel)

			// Replication-mode connections route MsgQuery through the
			// replication-command dispatcher instead of the regular
			// SQL path. The dispatcher recognises IDENTIFY_SYSTEM /
			// CREATE_REPLICATION_SLOT / DROP_REPLICATION_SLOT and the
			// (deferred) START_REPLICATION verbs; everything else
			// falls back to the normal handler so utility commands
			// like SHOW still work for diagnostics.
			if isReplication {
				// M0119-0006 (deferral row 1354 claim 2): thread the
				// connection's dbName down so the logical walsender can scope
				// every catalog lookup to the slot's database instead of the
				// DefaultDBOid fallback (DB 1).
				handled, err := s.replHandler.HandleCommand(ctx, r, w, f.Payload, appName, dbName)
				if err != nil {
					entry.clearQueryCancel()
					queryCancel()
					if errors.Is(err, errQueryErrorSent) {
						// Error + ReadyForQuery already sent cleanly.
						break
					}
					logger.Info("replication command write error", "err", err)
					return
				}
				if handled {
					entry.clearQueryCancel()
					queryCancel()
					break
				}
				// Fall through to the regular SQL path with the live
				// queryCtx still intact — PG's libpqrcv issues plain
				// SELECTs (pg_publication probes during CREATE
				// SUBSCRIPTION) on the same replication=database
				// connection and the cancellation must not fire until
				// handleQueryOrCopy completes.
			}
			// Arm the client-EOF watcher so a client that dies mid-query
			// (no CancelRequest ever arrives) cancels queryCtx instead of
			// leaving the backend computing for hours (csq-S6 deferral).
			// Replication connections manage their own socket lifecycle
			// and never arm it; the watcher is stopped the moment the
			// handler returns, before the loop reads the next frame.
			var eofWatch *clientEOFWatch
			if !isReplication {
				eofWatch = startClientEOFWatch(raw, queryCancel, logger)
			}
			nextCopyIn, err := s.handleQueryOrCopy(queryCtx, r, w, sess, f.Payload, connTx, prepStmts)
			eofWatch.Stop()
			entry.clearQueryCancel()
			queryCancel()
			if err != nil {
				if errors.Is(err, executor.ErrSelfTerminate) {
					// pg_terminate_backend(pg_backend_pid()): the query targeted
					// this backend. Emit the FATAL and close the connection,
					// matching PostgreSQL's SIGTERM-at-CHECK_FOR_INTERRUPTS path
					// (the client sees only the FATAL, no result row). The deferred
					// teardown (temp-object cleanup, advisory-lock release, open-txn
					// rollback) then runs before the socket closes. M0118-0009
					// (temp-schema-cleanup process-exit permutation).
					s.writeFatal(w, errcodes.AdminShutdown, "terminating connection due to administrator command")
					return
				}
				if errors.Is(err, errQueryErrorSent) {
					// Error + ReadyForQuery already sent cleanly; keep connection.
					// The client received the error and will send the next Query.
					break
				}
				logger.Info("query write error", "err", err)
				return
			}
			copyIn = nextCopyIn
		case libpq.MsgParse:
			em := s.handleParseFrame(extended, f.Payload)
			if em != nil {
				// M0132-S5: PostgreSQL aborts an open block from its top-level
				// error handler, so a Parse/Bind/Describe error inside a block
				// aborts it just as an Execute error does.
				failExplicitBlock(connTx)
				if err := s.writeExtendedMessageError(w, em); err != nil {
					return
				}
				extended.syncRequired = true
				break
			}
			if err := w.WriteParseComplete(); err != nil {
				return
			}
		case libpq.MsgBind:
			em := s.handleBindFrame(extended, f.Payload)
			if em != nil {
				failExplicitBlock(connTx) // M0132-S5, see MsgParse above
				if err := s.writeExtendedMessageError(w, em); err != nil {
					return
				}
				extended.syncRequired = true
				break
			}
			if err := w.WriteBindComplete(); err != nil {
				return
			}
		case libpq.MsgDescribe:
			em, err := s.handleDescribeFrame(extended, f.Payload, w, sess)
			if err != nil {
				return
			}
			if em != nil {
				failExplicitBlock(connTx) // M0132-S5, see MsgParse above
				if err := s.writeExtendedMessageError(w, em); err != nil {
					return
				}
				extended.syncRequired = true
			}
		case libpq.MsgExecute:
			queryCtx, queryCancel := context.WithCancel(ctx)
			entry.setQueryCancel(queryCancel)
			// Same client-EOF watcher as the MsgQuery path: an extended-
			// protocol Execute is where the long-running work happens.
			// MSG_PEEK never consumes, so frames the client pipelined
			// behind Execute (Sync etc.) are untouched.
			var eofWatch *clientEOFWatch
			if !isReplication {
				eofWatch = startClientEOFWatch(raw, queryCancel, logger)
			}
			em, err := s.handleExecuteFrame(queryCtx, extended, f.Payload, w, sess, connTx)
			eofWatch.Stop()
			entry.clearQueryCancel()
			queryCancel()
			if err != nil {
				return
			}
			if em != nil {
				if err := s.writeExtendedMessageError(w, em); err != nil {
					return
				}
				extended.syncRequired = true
			}
		case libpq.MsgClose:
			em := s.handleCloseFrame(extended, f.Payload)
			if em != nil {
				if err := s.writeExtendedMessageError(w, em); err != nil {
					return
				}
				extended.syncRequired = true
				break
			}
			if err := w.WriteCloseComplete(); err != nil {
				return
			}
		case libpq.MsgSync:
			extended.syncRequired = false
			// Deliver queued LISTEN/NOTIFY notifications at this command
			// boundary, before ReadyForQuery — mirroring the simple path's
			// deliverNotifications before its ReadyForQuery (dispatch.go:1113).
			// Without this a session that LISTENs over the extended protocol
			// would never receive an 'A' NotificationResponse. M0132-S12.
			if err := s.deliverNotifications(w, connTx); err != nil {
				return
			}
			if err := w.ReadyForQuery(); err != nil {
				return
			}
		case libpq.MsgFlush:
			// Flush itself carries no payload and no response frame.
		case libpq.MsgCopyData, libpq.MsgCopyDone, libpq.MsgCopyFail:
			// Accept but ignore, per protocol spec — upstream
			// postgres.c:5004-5013 ("we probably got here because a COPY
			// failed, and the frontend is still sending data"). A COPY FROM
			// that errors mid-stream reports ErrorResponse + RFQ right away
			// and clears copyIn, but the client only learns of the failure
			// after it has finished pushing; its trailing CopyData/CopyDone
			// frames then land here. Answering them (an ErrorResponse and a
			// second RFQ, as the default arm did) desynchronises the session
			// for every later statement, so they are dropped silently — no
			// ErrorResponse, and deliberately no ReadyForQuery.
		default:
			err = w.WriteErrorResponse([]libpq.ErrorField{
				{Code: libpq.FieldSeverity, Value: "ERROR"},
				{Code: libpq.FieldSeverityNonLocal, Value: "ERROR"},
				{Code: libpq.FieldSQLState, Value: string(errcodes.FeatureNotSupported)},
				{Code: libpq.FieldMessage, Value: fmt.Sprintf("message type %q not yet supported", f.Type)},
				{Code: libpq.FieldRoutine, Value: "postmaster.runPostStartupLoop"},
			})
			if err != nil {
				return
			}
			if err := w.ReadyForQueryAfterError(); err != nil {
				return
			}
		}
		if err := w.Flush(); err != nil {
			return
		}
	}
}

// writeFatal sends a single FATAL ErrorResponse and flushes. Errors are
// silently swallowed: by the time we're emitting FATAL, the connection is
// already going away.
func (s *Server) writeFatal(w *libpq.FrameWriter, code errcodes.Code, msg string) {
	_ = w.WriteErrorResponse([]libpq.ErrorField{
		{Code: libpq.FieldSeverity, Value: "FATAL"},
		{Code: libpq.FieldSeverityNonLocal, Value: "FATAL"},
		{Code: libpq.FieldSQLState, Value: string(code)},
		{Code: libpq.FieldMessage, Value: msg},
	})
	_ = w.Flush()
}

func newSecretKey() (uint32, error) {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, fmt.Errorf("generate cancel-key: %w", err)
	}
	return binary.BigEndian.Uint32(b[:]), nil
}
