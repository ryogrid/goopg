package server

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
)

// TestApplyLauncherStartsAndStopsOnDDLWake pins the core M0103-0002
// contract: after CREATE SUBSCRIPTION (modelled here by a direct
// PubSub call + Wake), the launcher spawns a worker within ≤1 s; after
// DROP SUBSCRIPTION (+ Wake), the worker exits before stopAll runs.
func TestApplyLauncherStartsAndStopsOnDDLWake(t *testing.T) {
	t.Parallel()

	ps := catalog.NewPubSub()
	var launched atomic.Int32
	stopCh := make(chan string, 8)

	launch := func(ctx context.Context, _ ApplyLauncherConfig, sub catalog.Subscription) error {
		launched.Add(1)
		<-ctx.Done()
		stopCh <- sub.Name
		return context.Cause(ctx)
	}

	l := NewApplyLauncher(ApplyLauncherConfig{
		PubSub:       ps,
		PollInterval: 50 * time.Millisecond,
		LaunchFn:     launch,
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	runDone := make(chan struct{})
	go func() {
		l.Run(ctx)
		close(runDone)
	}()

	// CREATE SUBSCRIPTION s1 ... WITH (enabled=true)
	if _, err := ps.CreateSubscription("s1", "host=127.0.0.1 port=5432", []string{"p"}, "", true); err != nil {
		t.Fatal(err)
	}
	l.Wake()

	if !waitFor(t, time.Second, func() bool { return launched.Load() >= 1 }) {
		t.Fatalf("worker never launched (launched=%d)", launched.Load())
	}
	if got := l.ActiveSubscriptions(); len(got) != 1 || got[0] != "s1" {
		t.Errorf("ActiveSubscriptions=%v want [s1]", got)
	}

	// DROP SUBSCRIPTION s1 — launcher must cancel the worker.
	if err := ps.DropSubscription("s1"); err != nil {
		t.Fatal(err)
	}
	l.Wake()

	select {
	case name := <-stopCh:
		if name != "s1" {
			t.Errorf("stopped worker name=%q want s1", name)
		}
	case <-time.After(time.Second):
		t.Fatal("worker did not stop after DROP + Wake within 1s")
	}

	if !waitFor(t, time.Second, func() bool {
		return len(l.ActiveSubscriptions()) == 0
	}) {
		t.Fatalf("ActiveSubscriptions=%v want empty", l.ActiveSubscriptions())
	}

	cancel()
	<-runDone
}

// TestApplyLauncherPollPicksUpEnabledSubscription verifies the
// periodic poll converges even when Wake is never called (e.g. the
// catalog was mutated by a path that doesn't carry the hook).
func TestApplyLauncherPollPicksUpEnabledSubscription(t *testing.T) {
	t.Parallel()

	ps := catalog.NewPubSub()
	var launched atomic.Int32
	launch := func(ctx context.Context, _ ApplyLauncherConfig, _ catalog.Subscription) error {
		launched.Add(1)
		<-ctx.Done()
		return context.Cause(ctx)
	}

	l := NewApplyLauncher(ApplyLauncherConfig{
		PubSub:       ps,
		PollInterval: 30 * time.Millisecond,
		LaunchFn:     launch,
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	runDone := make(chan struct{})
	go func() {
		l.Run(ctx)
		close(runDone)
	}()

	if _, err := ps.CreateSubscription("s2", "host=h", nil, "", true); err != nil {
		t.Fatal(err)
	}
	// No Wake — rely on the timer.

	if !waitFor(t, time.Second, func() bool { return launched.Load() >= 1 }) {
		t.Fatalf("poll never picked up subscription (launched=%d)", launched.Load())
	}

	cancel()
	<-runDone
}

// TestApplyLauncherIgnoresDisabledSubscription guards against spawning
// workers for `WITH (enabled = false)` rows. The disabled subscription
// must remain dormant until it flips to enabled.
func TestApplyLauncherIgnoresDisabledSubscription(t *testing.T) {
	t.Parallel()

	ps := catalog.NewPubSub()
	var launched atomic.Int32
	launch := func(ctx context.Context, _ ApplyLauncherConfig, _ catalog.Subscription) error {
		launched.Add(1)
		<-ctx.Done()
		return context.Cause(ctx)
	}

	l := NewApplyLauncher(ApplyLauncherConfig{
		PubSub:       ps,
		PollInterval: 30 * time.Millisecond,
		LaunchFn:     launch,
	})

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	runDone := make(chan struct{})
	go func() {
		l.Run(ctx)
		close(runDone)
	}()

	if _, err := ps.CreateSubscription("sd", "host=h", nil, "", false); err != nil {
		t.Fatal(err)
	}
	l.Wake()

	// Give the launcher two reconcile cycles. No worker should run.
	time.Sleep(150 * time.Millisecond)
	if got := launched.Load(); got != 0 {
		t.Errorf("disabled subscription spawned a worker (launched=%d)", got)
	}

	cancel()
	<-runDone
}

// TestApplyLauncherStopAllOnShutdown verifies that cancelling the
// launcher context cancels every worker context. Without this, a STOP
// from the control plane would leak background goroutines holding
// publisher TCP connections.
func TestApplyLauncherStopAllOnShutdown(t *testing.T) {
	t.Parallel()

	ps := catalog.NewPubSub()
	for _, name := range []string{"a", "b", "c"} {
		if _, err := ps.CreateSubscription(name, "host=h", nil, "", true); err != nil {
			t.Fatal(err)
		}
	}

	var mu sync.Mutex
	stopped := map[string]bool{}
	launch := func(ctx context.Context, _ ApplyLauncherConfig, sub catalog.Subscription) error {
		<-ctx.Done()
		mu.Lock()
		stopped[sub.Name] = true
		mu.Unlock()
		return context.Cause(ctx)
	}

	l := NewApplyLauncher(ApplyLauncherConfig{
		PubSub:       ps,
		PollInterval: 30 * time.Millisecond,
		LaunchFn:     launch,
	})
	ctx, cancel := context.WithCancel(context.Background())
	runDone := make(chan struct{})
	go func() {
		l.Run(ctx)
		close(runDone)
	}()

	// Wait for all three workers to be live.
	if !waitFor(t, time.Second, func() bool {
		return len(l.ActiveSubscriptions()) == 3
	}) {
		t.Fatalf("workers never converged; got %v", l.ActiveSubscriptions())
	}

	cancel()
	select {
	case <-runDone:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after cancel")
	}

	mu.Lock()
	defer mu.Unlock()
	for _, name := range []string{"a", "b", "c"} {
		if !stopped[name] {
			t.Errorf("worker %q did not exit on shutdown", name)
		}
	}
}

// TestApplyLauncherLaunchFnErrorIsLogged: a launch func that returns
// an error (other than context.Canceled) must not panic the launcher
// and the worker entry must be cleaned up on the next reconcile.
func TestApplyLauncherLaunchFnErrorIsLogged(t *testing.T) {
	t.Parallel()

	ps := catalog.NewPubSub()
	if _, err := ps.CreateSubscription("err", "host=h", nil, "", true); err != nil {
		t.Fatal(err)
	}

	failOnce := make(chan struct{}, 1)
	failOnce <- struct{}{}
	launch := func(ctx context.Context, _ ApplyLauncherConfig, _ catalog.Subscription) error {
		select {
		case <-failOnce:
			return errors.New("dial refused")
		default:
			<-ctx.Done()
			return context.Cause(ctx)
		}
	}

	l := NewApplyLauncher(ApplyLauncherConfig{
		PubSub:       ps,
		PollInterval: 30 * time.Millisecond,
		LaunchFn:     launch,
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	runDone := make(chan struct{})
	go func() {
		l.Run(ctx)
		close(runDone)
	}()

	// First call errors out fast; the second poll re-launches and
	// the worker stays live. ActiveSubscriptions must converge to
	// {"err"}.
	if !waitFor(t, time.Second, func() bool {
		return len(l.ActiveSubscriptions()) == 1
	}) {
		t.Fatalf("launcher did not retry after error; active=%v", l.ActiveSubscriptions())
	}

	cancel()
	<-runDone
}

// TestParseSubscriptionConninfo pins the libpq-style parser used by
// DefaultLaunchApplyWorker so future tweaks don't silently break
// publisher discovery.
func TestParseSubscriptionConninfo(t *testing.T) {
	cases := []struct {
		in       string
		wantAddr string
		wantApp  string
	}{
		{"", "", ""},
		{"host=10.0.0.1", "10.0.0.1:5432", ""},
		{"host=h port=6543", "h:6543", ""},
		{"host=h application_name=sub1", "h:5432", "sub1"},
		{"port=6543 application_name=alone", "", "alone"},
	}
	for _, c := range cases {
		gotAddr, gotApp := parseSubscriptionConninfo(c.in)
		if gotAddr != c.wantAddr || gotApp != c.wantApp {
			t.Errorf("parse(%q)=(%q,%q) want (%q,%q)",
				c.in, gotAddr, gotApp, c.wantAddr, c.wantApp)
		}
	}
}

// waitFor polls `cond` at 10 ms intervals until it returns true or the
// deadline elapses. Returns whether the condition fired.
func waitFor(t *testing.T, d time.Duration, cond func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cond()
}
