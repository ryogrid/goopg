// Package main is the goopg command-line entrypoint. It plays the combined
// role that PostgreSQL splits across initdb, postmaster, and pg_ctl: data
// directory initialization, server lifecycle, and the operator-facing
// commands that PostgreSQL drives via signals.
//
// See .ralph/specs/GOAL_AND_REQUIREMENTS.md §3.3 and §7.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/goopg/goopg/internal/auth"
	"github.com/goopg/goopg/internal/activity"
	"github.com/goopg/goopg/internal/config"
	"github.com/goopg/goopg/internal/control"
	"github.com/goopg/goopg/internal/initdb"
	"github.com/goopg/goopg/internal/server"
	"github.com/goopg/goopg/internal/wal"
)

const usage = `goopg — a Go reimplementation of PostgreSQL.

Usage:
  goopg <command> [arguments]

Commands:
  init       Initialize a data directory.
  start      Run the server in the foreground.
  stop       Request a graceful shutdown of a running server.
  restart    Stop the server and start it again.
  reload     Reload configuration without restarting.
  checkpoint Trigger a synchronous IMMEDIATE-speed checkpoint.
  promote    Promote a running standby to primary.
  status     Report whether a server is running and its high-level state.
  version    Print the goopg version and exit.

Use "goopg <command> -h" for command-specific flags.
`

type subcommand struct {
	name string
	run  func(args []string, stdout, stderr io.Writer) int
}

var subcommands = []subcommand{
	{"init", runInit},
	{"start", runStart},
	{"stop", runStop},
	{"restart", runRestart},
	{"reload", runReload},
	{"checkpoint", runCheckpoint},
	{"promote", runPromote},
	{"status", runStatus},
	{"version", runVersion},
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprint(stdout, usage)
		return 0
	}
	switch args[0] {
	case "-h", "--help", "help":
		fmt.Fprint(stdout, usage)
		return 0
	}
	for _, c := range subcommands {
		if c.name == args[0] {
			return c.run(args[1:], stdout, stderr)
		}
	}
	fmt.Fprintf(stderr, "goopg: unknown command %q\n\n", args[0])
	fmt.Fprint(stderr, usage)
	return 2
}

// notImplemented is the temporary handler for subcommands whose
// implementation is still pending. Each subcommand defines its own flag
// set up front so that "-h" already gives operators a stable surface to
// script against, and so that future loops only need to fill in the body.
func notImplemented(name string, fs *flag.FlagSet, args []string, stderr io.Writer) int {
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	fmt.Fprintf(stderr, "goopg %s: not yet implemented\n", name)
	return 1
}

func runInit(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("D", "", "data directory to initialize (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dataDir == "" {
		fmt.Fprintln(stderr, "goopg init: -D <data-directory> is required")
		return 2
	}
	if err := initdb.Init(initdb.Options{DataDir: *dataDir}); err != nil {
		fmt.Fprintf(stderr, "%v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "goopg init: created data directory at %s\n", *dataDir)
	return 0
}

func runStart(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("D", "", "data directory (created via 'goopg init'; when empty, the server runs without storage handles and only the protocol-only paths are reachable)")
	confPath := fs.String("config", "", "path to postgresql.conf (default: built-in defaults)")
	addr := fs.String("listen", "127.0.0.1:5432", "TCP listen address (host:port)")
	hbaPath := fs.String("hba", "", "path to pg_hba.conf (default: built-in loopback-trust policy)")
	if err := fs.Parse(args); err != nil {
		return 2
	}

	logger := slog.New(slog.NewTextHandler(stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// Start pprof HTTP endpoint on 127.0.0.1:6060 for CPU/heap profiling.
	// Available endpoints:
	//   /debug/pprof/profile?seconds=30  — CPU profile (download with go tool pprof)
	//   /debug/pprof/heap               — heap profile
	//   /debug/pprof/goroutine          — goroutine dump
	go func() {
		pprofAddr := "127.0.0.1:6060"
		ln, err := net.Listen("tcp", pprofAddr)
		if err != nil {
			logger.Debug("pprof listener not available", "addr", pprofAddr, "err", err)
			return
		}
		logger.Info("pprof endpoint", "addr", pprofAddr)
		if err := http.Serve(ln, nil); err != nil {
			logger.Debug("pprof server stopped", "err", err)
		}
	}()

	// Auto-discover postgresql.conf inside the data directory when the
	// operator didn't pass an explicit -config. Mirrors upstream
	// pg_ctl, which always reads `<datadir>/postgresql.conf`. Without
	// this default, a postgresql.conf entry the operator wrote (e.g.
	// `primary_conninfo` for a standby) is silently ignored — which
	// is the worst kind of "it just doesn't work."
	if *confPath == "" && *dataDir != "" {
		candidate := filepath.Join(*dataDir, "postgresql.conf")
		if _, err := os.Stat(candidate); err == nil {
			*confPath = candidate
		}
	}

	// SIGINT and SIGTERM both translate into the same internal shutdown
	// path (see docs/design/0001-architecture-overview.md §3). Other
	// operator-driven shutdowns will arrive over the control socket via
	// `goopg stop` once milestone 7 builds it.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	cfg := server.Config{
		Address: *addr,
		Logger:  logger,
	}

	// Build the GUC registry up front so server-context settings
	// (shared_buffers, etc.) are available before Open allocates the
	// buffer pool. Without a config file this is just the seeded
	// defaults; with one we apply the file's entries on top.
	registry := config.BuildDefaultRegistry()
	if *confPath != "" {
		entries, err := config.ParseConfigFile(*confPath)
		if err != nil {
			fmt.Fprintf(stderr, "goopg start: %v\n", err)
			return 1
		}
		if err := registry.ApplyConfigEntries(entries); err != nil {
			fmt.Fprintf(stderr, "goopg start: %v\n", err)
			return 1
		}
		cfg.Registry = registry
		logger.Info("loaded postgresql.conf", "path", *confPath, "entries", len(entries))
	}
	var rt *initdb.Runtime
	if *dataDir != "" {
		poolSlots := poolSlotsFromGUC(registry)
		walInitZero := boolGUC(registry, "wal_init_zero", true)
		walSenderMemBuf := int64(intGUC(registry, "wal_sender_memory_buffer", 16<<20))
		walBuffers := int64(intGUC(registry, "wal_buffers", 16<<20))
		walWriterDelayMS := intGUC(registry, "wal_writer_delay", 200)
		bgwriterDelayMS := intGUC(registry, "bgwriter_delay", 200)
		bgwriterMaxPages := intGUC(registry, "bgwriter_lru_maxpages", 100)
		aioMethod := stringGUC(registry, "io_method", "")
		aioWorkers := intGUC(registry, "io_workers", 0)
		aioMax := intGUC(registry, "io_max_concurrency", 0)
		var err error
		rt, err = initdb.Open(initdb.OpenOptions{
			DataDir:               *dataDir,
			PoolSlots:             poolSlots,
			WALInitZero:           walInitZero,
			WALSenderMemoryBuffer: walSenderMemBuf,
			WALBuffers:            walBuffers,
			WalWriterDelay:        time.Duration(walWriterDelayMS) * time.Millisecond,
			BgwriterDelay:         time.Duration(bgwriterDelayMS) * time.Millisecond,
			BgwriterMaxPages:      bgwriterMaxPages,
			AIOMethod:             aioMethod,
			AIOWorkers:            aioWorkers,
			AIOMaxConcurrency:     aioMax,
		})
		if err != nil {
			fmt.Fprintf(stderr, "goopg start: %v\n", err)
			return 1
		}
		// AIO engine startup line: surfaces the chosen method,
		// the worker count, and the in-flight cap so an operator
		// triaging an I/O-bound workload can verify the live
		// configuration without grepping through GUCs. Mirrors
		// the upstream "AIO method = ..." log line shape from
		// PostgreSQL 18.
		if rt.AIO != nil {
			// Probe-failure fallback line: io_uring on a kernel
			// that can't honour it (sysctl-disabled, ENOSYS,
			// EPERM under containerised seccomp) drops down to
			// worker silently inside aio.NewEngine. Surface it
			// here so an operator triaging a "but I asked for
			// io_uring" question can grep `event=aio_method_fallback`.
			if req, reason := rt.AIO.FallbackFrom(); req != "" {
				logger.Warn("aio method fallback",
					"event", "aio_method_fallback",
					"requested", req,
					"actual", rt.AIO.Method(),
					"reason", reason)
			}
			logger.Info("aio engine attached",
				"event", "aio_engine_attached",
				"method", rt.AIO.Method(),
				"workers", aioWorkers,
				"max_concurrency", aioMax)
		}
		// Walsender in-memory WAL handoff (M0010-0002): log ring
		// capacity at startup so an operator can verify the rollout.
		if rt.WAL != nil && rt.WAL.MemRing() != nil {
			logger.Info("wal sender memory buffer attached",
				"event", "wal_sender_memory_buffer_attached",
				"capacity_bytes", rt.WAL.MemRing().Cap())
		}
		// In-memory WAL buffer (M0013-0003): logged when
		// wal_buffers > 0 so an operator can grep
		// `event=wal_buffers_attached` to verify the rollout.
		// Suppressed when the buffer is disabled — there's
		// nothing to surface.
		if rt.WAL != nil {
			if cap := rt.WAL.WALBuffersCapacity(); cap > 0 {
				logger.Info("wal buffers attached",
					"event", "wal_buffers_attached",
					"capacity_bytes", cap)
			}
		}
		defer func() {
			// Persist the catalog before tearing down the pool.
			// If saving fails we surface a warning but still
			// close so file handles aren't leaked.
			if err := rt.SaveCatalog(); err != nil {
				logger.Warn("save catalog snapshot failed", "err", err)
			}
			_ = rt.Close()
		}()
		cfg.Catalog = rt.Catalog
		cfg.Pool = rt.Pool
		cfg.TxnMgr = rt.TxnMgr
		cfg.FSM = rt.FSM
		cfg.VM = rt.VM
		cfg.Checkpointer = rt.Checkpointer
		cfg.Slots = rt.Slots
		cfg.WalSenders = rt.WalSenders
		cfg.WAL = rt.WAL
		cfg.Activity = rt.Activity
		cfg.PubSub = rt.PubSub
		cfg.WALDirPath = filepath.Join(rt.DataDir, "pg_wal")
		cfg.DataDir = rt.DataDir
		logger.Info("opened data directory", "path", rt.DataDir, "shared_buffers_slots", poolSlots)
	}
	if *hbaPath != "" {
		policy, err := auth.ParseHBAFile(*hbaPath)
		if err != nil {
			fmt.Fprintf(stderr, "goopg start: %v\n", err)
			return 1
		}
		cfg.Policy = policy
		logger.Info("loaded pg_hba.conf", "path", *hbaPath, "rules", len(policy.Rules))
	}

	// Honour the checkpoint_timeout GUC (milestone 0002). Default of
	// 300s matches upstream; the prior hard-coded 10s was a
	// development convenience. A future reload path will reapply
	// this when the GUC changes — today the registry value is
	// frozen at startup.
	if rt != nil && rt.Checkpointer != nil {
		registry := cfg.Registry
		if registry == nil {
			registry = config.BuildDefaultRegistry()
		}
		if v, ok := registry.Get("checkpoint_timeout"); ok {
			if secs, err := strconv.Atoi(v.Display()); err == nil && secs > 0 {
				rt.Checkpointer.SetInterval(time.Duration(secs) * time.Second)
			}
		}
		// max_wal_size is stored in MB (matching upstream).
		// Convert to a byte threshold for the checkpointer's
		// volume-driven trigger.
		if v, ok := registry.Get("max_wal_size"); ok {
			if mb, err := strconv.Atoi(v.Display()); err == nil && mb > 0 {
				rt.Checkpointer.SetMaxWALBytes(uint64(mb) * 1024 * 1024)
			}
		}
		if v, ok := registry.Get("checkpoint_completion_target"); ok {
			if f, err := strconv.ParseFloat(v.Display(), 64); err == nil {
				rt.Checkpointer.SetCompletionTarget(f)
			}
		}
		if v, ok := registry.Get("full_page_writes"); ok && rt.Pool != nil {
			rt.Pool.SetFullPageWrites(v.Display() == "on")
		}

		// Slot-aware WAL retention. Wired here (rather than inside
		// initdb.Open) because the GUC values that govern its
		// behaviour — `max_slot_wal_keep_size` — only finish loading
		// once the postgresql.conf entries have been applied to the
		// registry. The retainer's own logger is the per-process
		// slog so retention events show up next to the rest of the
		// startup chatter.
		if rt.WAL != nil {
			retainer := &wal.SlotAwareRetainer{
				Writer: rt.WAL,
				Slots:  rt.Slots,
				Logger: logger,
			}
			if v, ok := registry.Get("max_slot_wal_keep_size"); ok {
				// Stored in MB (matching upstream guc_tables.c). -1
				// is the unlimited sentinel.
				if mb, err := strconv.Atoi(v.Display()); err == nil {
					if mb > 0 {
						retainer.MaxSlotKeepBytes = int64(mb) * 1024 * 1024
					}
				}
			}
			rt.Checkpointer.SetRetainer(retainer)
		}
	}

	// Run the periodic checkpointer alongside the server. SIGTERM /
	// SIGINT cancel `ctx` directly; a control-socket STOP makes
	// srv.Run return without cancelling ctx, so we drive the
	// checkpointer off a child context and cancel it on the way out
	// to avoid a stuck shutdown.
	cpCtx, cpCancel := context.WithCancel(ctx)
	cpDone := make(chan struct{})
	if rt != nil && rt.Checkpointer != nil {
		go func() {
			defer close(cpDone)
			// Register the checkpointer backend in the activity registry
			// so its wait events (CheckpointerMain) are visible.
			if act := rt.Activity; act != nil {
				cpPID := "cp-0"
				act.Register(&activity.Backend{
					PID:         cpPID,
					BackendType: "checkpointer",
					State:       "active",
					BackendStart: time.Now().UTC().Format(time.RFC3339Nano),
				})
				activity.RegisterCurrentGoroutine(act, cpPID)
				defer activity.ClearCurrentGoroutine()
			}
			_ = rt.Checkpointer.Run(cpCtx)
		}()
	} else {
		close(cpDone)
	}

	// Standby mode: when `<DataDir>/standby.signal` was present at
	// Open time, dial the configured `primary_conninfo` and spawn a
	// walreceiver + continuous-replay pair in the background. The
	// receiver streams WAL into the local writer (`<DataDir>/pg_wal`
	// ends up byte-identical to the primary's) and a streaming
	// `wal.StreamReplayer` consumes from a `RecordIterator` to apply
	// each record into data files as soon as it lands. Apply is
	// idempotent via `pd_lsn`, so on restart the iterator can safely
	// re-read the durable tail without bookkeeping a separate
	// apply cursor.
	//
	// The standbyController exposes a Promote method for the
	// control-plane PROMOTE command. It must be wired into
	// server.Config BEFORE srv.Run begins so the very first PROMOTE
	// after startup has a handler.
	var sc *standbyController
	if rt != nil && rt.Standby {
		sc = startStandby(ctx, rt, registry, logger)
		cfg.Promote = boundPromoteToServer(sc)
	}

	srv := server.New(cfg)
	runErr := srv.Run(ctx)
	cpCancel()
	if sc != nil {
		sc.Close()
	}
	<-cpDone
	if runErr != nil {
		fmt.Fprintf(stderr, "goopg start: %v\n", runErr)
		return 1
	}
	return 0
}

// startStandbyReplayer launches the continuous-replay goroutine that
// drains the local WAL writer into the storage manager. The standby
// runs both this and `startWalreceiver` together: the receiver
// produces WAL records (Append'ing into `rt.WAL`), and the replayer
// consumes from a streaming `wal.RecordIterator` and applies each
// record via `wal.ApplyRecord`. Apply is idempotent via `pd_lsn`, so
// the iterator anchors at the writer's current `WrittenLSN` (which
// equals the durable tail right after `initdb.Open`'s crash-recovery
// pass) and any record already on disk + applied is silently
// skipped.
//
// On a per-record apply error the goroutine logs and exits — a
// divergence between primary WAL and standby data files is
// unrecoverable without a fresh base backup, and continuing to apply
// would compound the corruption. The goroutine also exits when the
// supplied context is cancelled (clean shutdown) or the writer
// closes (process tear-down).
func startStandbyReplayer(ctx context.Context, done chan struct{}, rt *initdb.Runtime, logger *slog.Logger) *wal.StreamReplayer {
	baseLSN := rt.WAL.WrittenLSN()
	walDir := filepath.Join(rt.DataDir, "pg_wal")
	sr := wal.NewStreamReplayer(rt.StorageMgr, baseLSN)
	go func() {
		defer close(done)
		iter, err := wal.NewRecordIterator(rt.WAL, walDir, 0, baseLSN)
		if err != nil {
			logger.Error("standby replay: iterator init failed", "err", err)
			return
		}
		defer func() { _ = iter.Close() }()
		logger.Info("standby mode: starting continuous WAL replay",
			"start_lsn", baseLSN)
		if err := sr.Run(ctx, iter); err != nil {
			logger.Error("standby replay: stopped on error",
				"event", wal.EventStandbyReplayError,
				"apply_lsn", sr.ApplyLSN(), "err", err)
			return
		}
		logger.Info("standby replay: stopped",
			"event", wal.EventStandbyReplayStopped,
			"apply_lsn", sr.ApplyLSN())
	}()
	return sr
}

// startWalreceiver dials the primary identified by `primary_conninfo`
// and runs a `WalReceiver` in a goroutine, reconnecting with
// exponential backoff on transient failures. Returns once the
// goroutine is launched; the supplied `done` channel closes when
// the goroutine exits (after the parent context is cancelled).
//
// `primary_conninfo` is parsed as a libpq-style key=value bag; v0
// honours `host` + `port` (anything else is ignored). Empty conninfo
// is logged and the function returns without spawning — useful for
// integration tests that exercise the standby-mode boot path
// without an actual primary.
func startWalreceiver(ctx context.Context, done chan struct{}, rt *initdb.Runtime, registry *config.Registry, logger *slog.Logger) {
	conninfo := ""
	slotName := ""
	statusInterval := 10 * time.Second
	if registry != nil {
		if v, ok := registry.Get("primary_conninfo"); ok {
			conninfo = v.Display()
		}
		if v, ok := registry.Get("primary_slot_name"); ok {
			slotName = v.Display()
		}
		if v, ok := registry.Get("wal_receiver_status_interval"); ok {
			if secs, err := strconv.Atoi(v.Display()); err == nil && secs > 0 {
				statusInterval = time.Duration(secs) * time.Second
			}
		}
	}
	addr := parsePrimaryConninfo(conninfo)
	if addr == "" {
		logger.Info("standby mode: primary_conninfo empty or missing host:port; walreceiver not started")
		close(done)
		return
	}
	logger.Info("standby mode: starting walreceiver",
		"primary", addr, "slot", slotName, "status_interval", statusInterval)
	go func() {
		defer close(done)
		const baseBackoff = 500 * time.Millisecond
		const maxBackoff = 30 * time.Second
		backoff := baseBackoff
		for {
			if ctx.Err() != nil {
				return
			}
			rec, err := server.DialWalReceiver(ctx, server.WalReceiverConfig{
				PrimaryAddr:    addr,
				User:           "postgres",
				SlotName:       slotName,
				StartLSN:       rt.WAL.WrittenLSN(),
				WAL:            rt.WAL,
				StatusInterval: statusInterval,
				DialTimeout:    10 * time.Second,
				Receivers:      rt.WalReceivers,
				Conninfo:       conninfo,
			})
			if err != nil {
				logger.Warn("walreceiver dial failed; will retry",
					"event", wal.EventWalreceiverDialFailed,
					"primary", addr, "err", err, "backoff", backoff)
				select {
				case <-ctx.Done():
					return
				case <-time.After(backoff):
				}
				if backoff < maxBackoff {
					backoff *= 2
					if backoff > maxBackoff {
						backoff = maxBackoff
					}
				}
				continue
			}
			backoff = baseBackoff
			logger.Info("walreceiver connected",
				"event", wal.EventWalreceiverConnected,
				"primary", addr, "slot", slotName,
				"start_lsn", rt.WAL.WrittenLSN())
			runErr := rec.Run(ctx)
			lastApplied := rec.ApplyLSN()
			_ = rec.Close()
			if ctx.Err() != nil {
				return
			}
			if runErr != nil {
				logger.Warn("walreceiver disconnect; will reconnect",
					"event", wal.EventWalreceiverDisconnect,
					"primary", addr, "apply_lsn", lastApplied, "err", runErr)
			} else {
				logger.Info("walreceiver disconnect (clean); will reconnect",
					"event", wal.EventWalreceiverDisconnect,
					"primary", addr, "apply_lsn", lastApplied)
			}
		}
	}()
}

// parsePrimaryConninfo extracts the host:port from a libpq-style
// `key=value [key=value ...]` conninfo string. Defaults port to 5432
// when host is given without one. Returns "" when no host is
// provided. v0 honours only host + port; user / password / sslmode
// follow in later loops.
func parsePrimaryConninfo(conninfo string) string {
	conninfo = strings.TrimSpace(conninfo)
	if conninfo == "" {
		return ""
	}
	host := ""
	port := "5432"
	for _, tok := range strings.Fields(conninfo) {
		eq := strings.IndexByte(tok, '=')
		if eq < 0 {
			continue
		}
		k := strings.ToLower(tok[:eq])
		v := tok[eq+1:]
		switch k {
		case "host":
			host = v
		case "port":
			port = v
		}
	}
	if host == "" {
		return ""
	}
	return host + ":" + port
}

// boolGUC reads a boolean GUC by name, returning fallback when the
// registry is nil, the variable isn't registered, or the value
// can't be parsed.
func boolGUC(registry *config.Registry, name string, fallback bool) bool {
	if registry == nil {
		return fallback
	}
	v, ok := registry.Get(name)
	if !ok {
		return fallback
	}
	switch strings.ToLower(strings.TrimSpace(v.Display())) {
	case "on", "true", "yes", "1":
		return true
	case "off", "false", "no", "0":
		return false
	}
	return fallback
}

// stringGUC reads a string-shaped GUC by name, returning fallback
// when the registry is nil, the variable isn't registered, or its
// Display value is empty.
func stringGUC(registry *config.Registry, name string, fallback string) string {
	if registry == nil {
		return fallback
	}
	v, ok := registry.Get(name)
	if !ok {
		return fallback
	}
	if d := strings.TrimSpace(v.Display()); d != "" {
		return d
	}
	return fallback
}

// intGUC reads an int-shaped GUC by name, returning fallback when
// the registry is nil, the variable isn't registered, or its
// Display value isn't a valid integer.
func intGUC(registry *config.Registry, name string, fallback int) int {
	if registry == nil {
		return fallback
	}
	v, ok := registry.Get(name)
	if !ok {
		return fallback
	}
	n, err := strconv.Atoi(strings.TrimSpace(v.Display()))
	if err != nil {
		return fallback
	}
	return n
}

// poolSlotsFromGUC reads the shared_buffers GUC (canonical KB) and
// converts it to a buffer-pool slot count. shared_buffers/8 because
// each slot is BlockSize=8 KB. Returns 0 (which Open treats as "use
// default") if the GUC isn't present, isn't parseable, or yields a
// non-positive count — the registry guarantees a value at boot, so
// the fallback is purely defensive.
func poolSlotsFromGUC(registry *config.Registry) int {
	if registry == nil {
		return 0
	}
	v, ok := registry.Get("shared_buffers")
	if !ok {
		return 0
	}
	kb, err := strconv.Atoi(v.Display())
	if err != nil || kb <= 0 {
		return 0
	}
	slots := kb / 8
	if slots <= 0 {
		return 0
	}
	return slots
}

func runStop(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("stop", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("D", "", "data directory of the server to stop (required)")
	mode := fs.String("mode", "fast", "shutdown mode: smart|fast|immediate (v0 treats all three as graceful)")
	timeoutSec := fs.Int("t", 30, "seconds to wait for shutdown to complete")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dataDir == "" {
		fmt.Fprintln(stderr, "goopg stop: -D <data-directory> is required")
		return 2
	}
	pf, err := control.ParsePIDFile(*dataDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(stderr, "goopg stop: server not running (no postmaster.pid)")
			return 1
		}
		fmt.Fprintf(stderr, "goopg stop: %v\n", err)
		return 1
	}
	// v0 collapses smart/fast/immediate onto the same graceful
	// path; the flag is preserved so future loops can split them.
	_ = *mode
	reply, err := control.Send(pf.SocketPath, "STOP", time.Duration(*timeoutSec)*time.Second)
	if err != nil {
		fmt.Fprintf(stderr, "goopg stop: %v\n", err)
		return 1
	}
	if reply != "OK" {
		fmt.Fprintf(stderr, "goopg stop: unexpected reply %q\n", reply)
		return 1
	}
	// Wait for the process to actually exit so a follow-up
	// `goopg start` doesn't race with the previous incarnation.
	deadline := time.Now().Add(time.Duration(*timeoutSec) * time.Second)
	for time.Now().Before(deadline) {
		if !control.ProcessAlive(pf.PID) {
			fmt.Fprintln(stdout, "goopg stop: server stopped")
			return 0
		}
		time.Sleep(50 * time.Millisecond)
	}
	fmt.Fprintf(stderr, "goopg stop: server did not exit within %ds\n", *timeoutSec)
	return 1
}

func runRestart(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("restart", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	// Restarting a foreground-supervised server is the supervisor's
	// job (systemd, container runtime); v0's `goopg restart` is
	// effectively the same as `goopg stop`. Documenting this here
	// rather than removing the subcommand keeps the CLI shape
	// matching pg_ctl for muscle memory.
	fmt.Fprintln(stderr, "goopg restart: not yet implemented; use 'goopg stop -D ...' then 'goopg start -D ...' (v0 runs in the foreground; the operator's supervisor restarts the process)")
	return 1
}

func runReload(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("reload", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("D", "", "data directory of the server to reload (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dataDir == "" {
		fmt.Fprintln(stderr, "goopg reload: -D <data-directory> is required")
		return 2
	}
	pf, err := control.ParsePIDFile(*dataDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(stderr, "goopg reload: server not running (no postmaster.pid)")
			return 1
		}
		fmt.Fprintf(stderr, "goopg reload: %v\n", err)
		return 1
	}
	reply, err := control.Send(pf.SocketPath, "RELOAD", 5*time.Second)
	if err != nil {
		fmt.Fprintf(stderr, "goopg reload: %v\n", err)
		return 1
	}
	if reply != "OK" {
		fmt.Fprintf(stderr, "goopg reload: unexpected reply %q\n", reply)
		return 1
	}
	fmt.Fprintln(stdout, "goopg reload: configuration reload signalled (v0 no-op)")
	return 0
}

// runCheckpoint sends a CHECKPOINT request over the control
// socket. Same semantics as the SQL `CHECKPOINT` verb — a
// synchronous IMMEDIATE-speed checkpoint that returns once the
// marker is durable. The default 5-min timeout matches the upper
// end of typical bgwriter cycles; operators can extend it for
// big buffer pools.
func runCheckpoint(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("checkpoint", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("D", "", "data directory of the server to checkpoint (required)")
	timeoutSec := fs.Int("t", 300, "seconds to wait for the checkpoint to complete")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dataDir == "" {
		fmt.Fprintln(stderr, "goopg checkpoint: -D <data-directory> is required")
		return 2
	}
	pf, err := control.ParsePIDFile(*dataDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(stderr, "goopg checkpoint: server not running (no postmaster.pid)")
			return 1
		}
		fmt.Fprintf(stderr, "goopg checkpoint: %v\n", err)
		return 1
	}
	reply, err := control.Send(pf.SocketPath, "CHECKPOINT", time.Duration(*timeoutSec)*time.Second)
	if err != nil {
		fmt.Fprintf(stderr, "goopg checkpoint: %v\n", err)
		return 1
	}
	if reply != "OK" {
		fmt.Fprintf(stderr, "goopg checkpoint: %s\n", reply)
		return 1
	}
	fmt.Fprintln(stdout, "goopg checkpoint: complete")
	return 0
}

// runPromote sends a PROMOTE request over the control socket and
// blocks until the server confirms the standby has been drained,
// `standby.signal` removed, and the runtime flipped to primary
// mode. Default 5-min timeout matches `runCheckpoint`'s — promotion
// is bounded by `drainTimeout` (5s server-side), but the operator
// may have a deeper queue of WAL still in flight from the primary,
// so a generous client-side timeout is the safe default.
func runPromote(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("promote", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("D", "", "data directory of the standby to promote (required)")
	timeoutSec := fs.Int("t", 300, "seconds to wait for the promotion to complete")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dataDir == "" {
		fmt.Fprintln(stderr, "goopg promote: -D <data-directory> is required")
		return 2
	}
	pf, err := control.ParsePIDFile(*dataDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(stderr, "goopg promote: server not running (no postmaster.pid)")
			return 1
		}
		fmt.Fprintf(stderr, "goopg promote: %v\n", err)
		return 1
	}
	reply, err := control.Send(pf.SocketPath, "PROMOTE", time.Duration(*timeoutSec)*time.Second)
	if err != nil {
		fmt.Fprintf(stderr, "goopg promote: %v\n", err)
		return 1
	}
	if reply != "OK" {
		fmt.Fprintf(stderr, "goopg promote: %s\n", reply)
		return 1
	}
	fmt.Fprintln(stdout, "goopg promote: complete")
	return 0
}

func runStatus(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("D", "", "data directory of the server to inspect (required)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *dataDir == "" {
		fmt.Fprintln(stderr, "goopg status: -D <data-directory> is required")
		return 2
	}
	pf, err := control.ParsePIDFile(*dataDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			fmt.Fprintln(stdout, "goopg status: not running")
			// Match pg_ctl status's "exit 3 = not running" semantics
			// so shell scripts can branch on the exit code.
			return 3
		}
		fmt.Fprintf(stderr, "goopg status: %v\n", err)
		return 1
	}
	if !control.ProcessAlive(pf.PID) {
		fmt.Fprintf(stdout, "goopg status: not running (stale postmaster.pid for pid %d)\n", pf.PID)
		return 3
	}
	// Best-effort liveness ping over the control socket. A failure
	// here means the process is up but unresponsive — distinguish
	// from "stopped" so operators can act differently.
	if reply, err := control.Send(pf.SocketPath, "PING", 2*time.Second); err == nil && reply == "OK" {
		fmt.Fprintf(stdout, "goopg status: running (pid %d, listen %s, started %s)\n",
			pf.PID, pf.ListenAddr, pf.StartedAt.Format(time.RFC3339))
		return 0
	}
	fmt.Fprintf(stdout, "goopg status: process %d alive but control socket %s not responding\n",
		pf.PID, pf.SocketPath)
	return 4
}

// version is the human-readable build tag for the goopg binary. The reported
// PostgreSQL-compatibility wire version is a separate concern and lives in
// the protocol layer (see docs/design/0001-architecture-overview.md).
const version = "0.0.0-dev"

func runVersion(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("version", flag.ContinueOnError)
	fs.SetOutput(stderr)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	fmt.Fprintf(stdout, "goopg %s\n", version)
	return 0
}
