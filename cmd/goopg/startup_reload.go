package main

import (
	"log/slog"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"

	"github.com/goopg/goopg/internal/initdb"
	"github.com/goopg/goopg/internal/postmaster"
	"github.com/goopg/goopg/internal/utils/misc"
)

// startupReload is goopg start's SIGHUP handling before the server's own
// reload handler (postmaster.Server, installed when the listener starts)
// exists — the span covering crash recovery, archive recovery and the
// promotion that follows it.
//
// Upstream's postmaster installs its SIGHUP handler before it forks the
// startup process, and the startup process's handler only sets
// got_SIGHUP; ProcessStartupProcInterrupts then runs
// ProcessConfigFile(PGC_SIGHUP) at its next interrupt check, so the reload
// lands between recovery steps rather than concurrently with them. The
// signal goroutine here likewise only sets a flag, and process — called at
// the recovery loop's fetch boundary and once more before the server
// starts — applies the reload synchronously on the startup goroutine, so
// registry reads during recovery never race a concurrent reload.
//
// Without it Go's default action for SIGHUP terminates the process, and
// since postmaster.pid now exists from the start (CreateDataDirLockFile),
// `pg_ctl reload` during a long archive recovery would kill the server.
type startupReload struct {
	logger  *slog.Logger
	ch      chan os.Signal
	done    chan struct{}
	pending atomic.Bool
}

func newStartupReload(logger *slog.Logger) *startupReload {
	r := &startupReload{logger: logger, ch: make(chan os.Signal, 1), done: make(chan struct{})}
	signal.Notify(r.ch, syscall.SIGHUP)
	go func() {
		for {
			select {
			case <-r.done:
				return
			case <-r.ch:
				r.pending.Store(true)
			}
		}
	}()
	return r
}

// stop unregisters the handler at goopg start's exit.
func (r *startupReload) stop() {
	signal.Stop(r.ch)
	close(r.done)
}

// process applies a reload flagged since the last call: postgresql.conf
// (configPath, skipped when empty) then postgresql.auto.conf, through the
// same ApplyReloadEntries the running server's reload uses. A file that
// fails to parse keeps the old values, as ProcessConfigFile does.
func (r *startupReload) process(registry *misc.Registry, configPath, dataDir string) {
	if r == nil || !r.pending.CompareAndSwap(true, false) {
		return
	}
	r.logger.Info("received SIGHUP, reloading configuration files")
	if registry == nil {
		return
	}
	entries, err := misc.ReloadConfigEntries(configPath, dataDir)
	if err != nil {
		r.logger.Error("configuration file contains errors; no changes were applied", "path", configPath, "err", err)
		return
	}
	result := registry.ApplyReloadEntries(entries)
	for _, w := range result.Warnings {
		r.logger.Warn("reload", "issue", w)
	}
	r.logger.Info("reload complete", "path", configPath, "changed", result.Changed, "warnings", len(result.Warnings))
}

// immediateShutdown is goopg start's SIGQUIT handling — the postmaster's
// immediate shutdown (pmdie SIGQUIT → "received immediate shutdown
// request", pg_ctl stop -m immediate). It is registered for the whole life
// of goopg start, so Go's default SIGQUIT action (dump every goroutine and
// exit 2, leaving postmaster.pid behind) never runs:
//   - before the server runs (crash or archive recovery), it marks the
//     runtime so its Close writes no shutdown checkpoint, records the
//     request, and cancels the start context — archive recovery stops at
//     its next check, and goopg start returns without promoting and
//     without touching pg_control's state;
//   - once the server runs, it is Server.StopImmediate, the same path as
//     `goopg stop -mode immediate`.
type immediateShutdown struct {
	logger    *slog.Logger
	cancel    func()
	ch        chan os.Signal
	done      chan struct{}
	requested atomic.Bool
	rt        atomic.Pointer[initdb.Runtime]
	srv       atomic.Pointer[postmaster.Server]
}

func newImmediateShutdown(logger *slog.Logger, cancel func()) *immediateShutdown {
	q := &immediateShutdown{logger: logger, cancel: cancel, ch: make(chan os.Signal, 1), done: make(chan struct{})}
	signal.Notify(q.ch, syscall.SIGQUIT)
	go func() {
		for {
			select {
			case <-q.done:
				return
			case <-q.ch:
				q.trigger()
			}
		}
	}()
	return q
}

// trigger runs one immediate shutdown request.
func (q *immediateShutdown) trigger() {
	if !q.requested.CompareAndSwap(false, true) {
		return
	}
	if srv := q.srv.Load(); srv != nil {
		srv.StopImmediate("received immediate shutdown request")
	} else {
		q.logger.Info("received immediate shutdown request")
		if rt := q.rt.Load(); rt != nil {
			rt.SetImmediateShutdown()
		}
	}
	q.cancel()
}

// setRuntime hands the opened runtime over; a request that arrived while it
// was opening (crash recovery) is applied to it now.
func (q *immediateShutdown) setRuntime(rt *initdb.Runtime) {
	q.rt.Store(rt)
	if q.requested.Load() && rt != nil {
		rt.SetImmediateShutdown()
	}
}

// setServer hands the built server over before it runs.
func (q *immediateShutdown) setServer(srv *postmaster.Server) { q.srv.Store(srv) }

// Requested reports whether an immediate shutdown was requested.
func (q *immediateShutdown) Requested() bool { return q != nil && q.requested.Load() }

func (q *immediateShutdown) stop() {
	signal.Stop(q.ch)
	close(q.done)
}
