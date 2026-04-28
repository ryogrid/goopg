// Package main is the goopg command-line entrypoint. It plays the combined
// role that PostgreSQL splits across initdb, postmaster, and pg_ctl: data
// directory initialization, server lifecycle, and the operator-facing
// commands that PostgreSQL drives via signals.
//
// See .ralph/specs/GOAL_AND_REQUIREMENTS.md §3.3 and §7.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/goopg/goopg/internal/auth"
	"github.com/goopg/goopg/internal/config"
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
		defer func() { _ = rt.Close() }()
		cfg.Catalog = rt.Catalog
		cfg.Pool = rt.Pool
		cfg.TxnMgr = rt.TxnMgr
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
	if err := srv.Run(ctx); err != nil {
		fmt.Fprintf(stderr, "goopg start: %v\n", err)
		return 1
	}
	return 0
}

func runStop(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("stop", flag.ContinueOnError)
	fs.String("D", "", "data directory of the server to stop")
	fs.String("mode", "fast", "shutdown mode: smart|fast|immediate")
	return notImplemented("stop", fs, args, stderr)
}

func runRestart(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("restart", flag.ContinueOnError)
	fs.String("D", "", "data directory")
	fs.String("mode", "fast", "shutdown mode for the stop phase")
	return notImplemented("restart", fs, args, stderr)
}

func runReload(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("reload", flag.ContinueOnError)
	fs.String("D", "", "data directory of the server to reload")
	return notImplemented("reload", fs, args, stderr)
}

func runStatus(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("status", flag.ContinueOnError)
	fs.String("D", "", "data directory of the server to inspect")
	return notImplemented("status", fs, args, stderr)
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
