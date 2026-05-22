package testport

import (
	"context"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testport/framework"
	"github.com/goopg/goopg/internal/testutil/cluster"
)

func TestDebugSpecconflictActualOutput(t *testing.T) {
	root := repoRoot(t)
	c := newCluster(t, "iso_spec_debug")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	runner := &framework.IsolationRunner{DSN: buildDSN(t, c)}
	spec, err := framework.ParseIsolationSpec(root + "/postgres/src/test/isolation/specs/insert-conflict-specconflict.spec")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	out, err := runner.RunSpec(ctx, spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Fatalf("debug output:\n%s", out)
}