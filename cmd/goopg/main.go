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
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/goopg/goopg/internal/auth"
	"github.com/goopg/goopg/internal/config"
	"github.com/goopg/goopg/internal/control"
	"github.com/goopg/goopg/internal/initdb"
	"github.com/goopg/goopg/internal/server"
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
	var rt *initdb.Runtime
	if *dataDir != "" {
		var err error
		rt, err = initdb.Open(initdb.OpenOptions{DataDir: *dataDir})
		if err != nil {
			fmt.Fprintf(stderr, "goopg start: %v\n", err)
			return 1
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
		cfg.Checkpointer = rt.Checkpointer
		cfg.DataDir = rt.DataDir
		logger.Info("opened data directory", "path", rt.DataDir)
	}
	if *confPath != "" {
		entries, err := config.ParseConfigFile(*confPath)
		if err != nil {
			fmt.Fprintf(stderr, "goopg start: %v\n", err)
			return 1
		}
		registry := config.BuildDefaultRegistry()
		if err := registry.ApplyConfigEntries(entries); err != nil {
			fmt.Fprintf(stderr, "goopg start: %v\n", err)
			return 1
		}
		cfg.Registry = registry
		logger.Info("loaded postgresql.conf", "path", *confPath, "entries", len(entries))
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

	srv := server.New(cfg)

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
			_ = rt.Checkpointer.Run(cpCtx)
		}()
	} else {
		close(cpDone)
	}

	runErr := srv.Run(ctx)
	cpCancel()
	<-cpDone
	if runErr != nil {
		fmt.Fprintf(stderr, "goopg start: %v\n", runErr)
		return 1
	}
	return 0
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
