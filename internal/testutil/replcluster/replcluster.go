// Package replcluster orchestrates a primary + standby goopg pair
// for end-to-end replication acceptance tests. Mirrors the API shape
// of `internal/testutil/cluster` so test code that already drives a
// single cluster needs minimal new vocabulary to drive a pair.
//
// The harness wires up the v0 streaming-replication path end-to-end:
//
//  1. Init two data directories.
//  2. Pre-create a physical replication slot on the primary (offline,
//     by writing the slot state file directly — `wal.OpenSlots`
//     speaks JSON on disk, so the on-disk slot is identical to one
//     created via the wire-level CREATE_REPLICATION_SLOT command).
//  3. Start the primary.
//  4. Stop the primary, copy its data directory to the standby's
//     location, then add `standby.signal` and a postgresql.conf
//     entry pointing the standby at the primary via primary_conninfo.
//  5. Restart the primary.
//  6. Start the standby; the auto-spawned walreceiver dials the
//     primary, the slot becomes active, and WAL streams.
//
// v0 doesn't ship `pg_basebackup`, so step 4's offline copy is the
// substitute. Once base-backup-over-the-wire lands, this harness
// will gain a `BaseBackup()` method that drives the streaming clone
// instead.
//
// See docs/design/0005-0006-replcluster-harness.md.
package replcluster

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/wal"
)

// Options controls cluster pair creation.
type Options struct {
	// RepoRoot is the goopg repository root (the directory holding
	// `go.mod`). Required so the harness can `go run ./cmd/goopg`.
	RepoRoot string
	// BaseDir is the tempdir under which both clusters' data
	// directories live. Optional — defaults to a fresh tempdir
	// under <RepoRoot>/tmp/replclusters/<name>.
	BaseDir string
	// SlotName is the physical replication slot the standby will
	// connect against. Defaults to "standby_slot".
	SlotName string
	// StartupWait / ShutdownWait are forwarded to the underlying
	// single-cluster harness. Defaults match cluster.Options.
	StartupWait  time.Duration
	ShutdownWait time.Duration
}

// ReplCluster is the primary+standby pair handle.
type ReplCluster struct {
	Primary  *cluster.Cluster
	Standby  *cluster.Cluster
	SlotName string
	repoRoot string
}

// New constructs the pair. Both clusters get their own fresh data
// dirs and a `127.0.0.1:0` listener (the harness picks free ports).
func New(name string, opts Options) (*ReplCluster, error) {
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("replcluster: name is required")
	}
	if strings.TrimSpace(opts.RepoRoot) == "" {
		return nil, errors.New("replcluster: RepoRoot is required")
	}
	baseDir := opts.BaseDir
	if strings.TrimSpace(baseDir) == "" {
		baseDir = filepath.Join(opts.RepoRoot, "tmp", "replclusters", name)
	}
	slotName := opts.SlotName
	if strings.TrimSpace(slotName) == "" {
		slotName = "standby_slot"
	}

	primary, err := cluster.New(name+"-primary", cluster.Options{
		RepoRoot:     opts.RepoRoot,
		DataDir:      filepath.Join(baseDir, "primary"),
		StartupWait:  opts.StartupWait,
		ShutdownWait: opts.ShutdownWait,
	})
	if err != nil {
		return nil, fmt.Errorf("replcluster: primary: %w", err)
	}
	standby, err := cluster.New(name+"-standby", cluster.Options{
		RepoRoot:     opts.RepoRoot,
		DataDir:      filepath.Join(baseDir, "standby"),
		StartupWait:  opts.StartupWait,
		ShutdownWait: opts.ShutdownWait,
	})
	if err != nil {
		return nil, fmt.Errorf("replcluster: standby: %w", err)
	}
	return &ReplCluster{
		Primary:  primary,
		Standby:  standby,
		SlotName: slotName,
		repoRoot: opts.RepoRoot,
	}, nil
}

// Setup runs the full primary→standby bootstrap. After Setup
// returns, both clusters are running and the standby's walreceiver
// is dialing the primary's slot. Steps mirror the package-level
// summary above.
func (r *ReplCluster) Setup() error {
	// 1. Init primary; pre-create the slot on its data dir.
	if err := r.Primary.Init(); err != nil {
		return fmt.Errorf("replcluster: primary init: %w", err)
	}
	slots, err := wal.OpenSlots(r.Primary.DataDir())
	if err != nil {
		return fmt.Errorf("replcluster: open primary slots: %w", err)
	}
	// startLSN=0 lets the walreceiver request from the very
	// beginning. The slot's RestartLSN advances as the standby
	// confirms flushed bytes.
	if _, err := slots.Create(r.SlotName, wal.SlotPhysical, 0); err != nil &&
		!errors.Is(err, wal.ErrSlotExists) {
		return fmt.Errorf("replcluster: pre-create slot: %w", err)
	}

	// 2. Start primary so it lays down the initial WAL segment + any
	//    other startup files we want to clone.
	if err := r.Primary.Start(); err != nil {
		return fmt.Errorf("replcluster: primary start: %w", err)
	}

	// 3. Stop primary cleanly so the on-disk state is durable, then
	//    copy it to the standby's data dir.
	if err := r.Primary.Stop(cluster.ShutdownFast); err != nil {
		return fmt.Errorf("replcluster: primary stop for clone: %w", err)
	}
	if err := r.Standby.Init(); err != nil {
		return fmt.Errorf("replcluster: standby init: %w", err)
	}
	if err := cloneDataDir(r.Primary.DataDir(), r.Standby.DataDir()); err != nil {
		return fmt.Errorf("replcluster: clone primary→standby: %w", err)
	}

	// 4. Stamp the standby with standby.signal + a primary_conninfo
	//    pointer. We can't use AppendPostgresqlConf for the slot
	//    name because the standby reads its conf at start time; we
	//    write both conninfo lines together to keep them visible to
	//    the boot path.
	if err := os.WriteFile(filepath.Join(r.Standby.DataDir(), "standby.signal"), nil, 0o600); err != nil {
		return fmt.Errorf("replcluster: write standby.signal: %w", err)
	}
	primaryAddr := r.Primary.ListenAddr()
	host, port, err := splitHostPort(primaryAddr)
	if err != nil {
		return fmt.Errorf("replcluster: parse primary addr: %w", err)
	}
	conninfoLine := fmt.Sprintf("primary_conninfo = 'host=%s port=%s'", host, port)
	slotLine := fmt.Sprintf("primary_slot_name = '%s'", r.SlotName)
	if err := r.Standby.AppendPostgresqlConf(conninfoLine); err != nil {
		return fmt.Errorf("replcluster: append conninfo: %w", err)
	}
	if err := r.Standby.AppendPostgresqlConf(slotLine); err != nil {
		return fmt.Errorf("replcluster: append slot name: %w", err)
	}

	// 5. Restart primary, then start standby. Order matters: the
	//    walreceiver immediately dials the primary on standby boot.
	if err := r.Primary.Start(); err != nil {
		return fmt.Errorf("replcluster: primary restart: %w", err)
	}
	if err := r.Standby.Start(); err != nil {
		// Best-effort cleanup so a failed Setup leaves a tidy
		// process tree.
		_ = r.Primary.Stop(cluster.ShutdownImmediate)
		return fmt.Errorf("replcluster: standby start: %w", err)
	}
	return nil
}

// Stop gracefully tears down both clusters. Errors are joined so a
// transient standby-side failure doesn't hide a primary-side one.
func (r *ReplCluster) Stop() error {
	var errs []string
	if err := r.Standby.Stop(cluster.ShutdownFast); err != nil {
		errs = append(errs, "standby: "+err.Error())
	}
	if err := r.Primary.Stop(cluster.ShutdownFast); err != nil {
		errs = append(errs, "primary: "+err.Error())
	}
	if len(errs) > 0 {
		return fmt.Errorf("replcluster: stop: %s", strings.Join(errs, "; "))
	}
	return nil
}

// Promote drives the standby's PROMOTE control command via
// `goopg promote -D <standby data dir>`. Returns once the promotion
// is durable (standby.signal removed; standby is now primary).
func (r *ReplCluster) Promote() error {
	cmd := exec.Command("go", "run", "./cmd/goopg", "promote", "-D", r.Standby.DataDir())
	cmd.Dir = r.repoRoot
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("replcluster: promote: %w (output=%s)", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// cloneDataDir mirrors `cp -a src/. dst/` for the kinds of files
// goopg's data directory contains: regular files, empty
// directories, and symlinks (none today, but if a future loop adds
// them they Just Work). Skips any pre-existing files in dst that
// would otherwise conflict — the standby's freshly-init'd dir has
// PG_VERSION, postgresql.conf, etc. that match the primary's; we
// overwrite them with the primary's copies so the standby starts
// from byte-identical state.
func cloneDataDir(src, dst string) error {
	return filepath.Walk(src, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		// Skip the standby controller's per-process state. The
		// pidfile + control socket are owned by the running primary
		// and would race the standby on boot if copied.
		if rel == "postmaster.pid" || rel == ".goopg.ctl.sock" {
			return nil
		}
		target := filepath.Join(dst, rel)
		if info.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		// Regular file: copy contents + mode.
		s, err := os.Open(path)
		if err != nil {
			return err
		}
		defer s.Close()
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		d, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return err
		}
		defer d.Close()
		if _, err := io.Copy(d, s); err != nil {
			return err
		}
		return nil
	})
}

func splitHostPort(addr string) (string, string, error) {
	idx := strings.LastIndex(addr, ":")
	if idx < 0 {
		return "", "", fmt.Errorf("missing port in %q", addr)
	}
	return addr[:idx], addr[idx+1:], nil
}
