package pubsubcluster

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/testutil/cluster"
	"github.com/goopg/goopg/internal/testutil/pgcluster"
)

// goopgPeer adapts `*cluster.Cluster` (the goopg harness) to ReplPeer.
type goopgPeer struct {
	c *cluster.Cluster
}

func (g *goopgPeer) Kind() ClusterKind { return ClusterKindGoopg }
func (g *goopgPeer) Host() string {
	host, _, _ := splitHostPort(g.c.ListenAddr())
	return host
}
func (g *goopgPeer) Port() int {
	_, p, _ := splitHostPort(g.c.ListenAddr())
	port, _ := atoi(p)
	return port
}
func (g *goopgPeer) User() string     { return "postgres" }
func (g *goopgPeer) Database() string { return "postgres" }
func (g *goopgPeer) Conninfo(applicationName string) string {
	base := fmt.Sprintf("host=%s port=%d user=%s dbname=%s",
		g.Host(), g.Port(), g.User(), g.Database())
	if strings.TrimSpace(applicationName) != "" {
		base += " application_name=" + applicationName
	}
	return base
}
func (g *goopgPeer) Start() error { return g.c.Start() }
func (g *goopgPeer) Stop() error  { return g.c.Stop(cluster.ShutdownFast) }
func (g *goopgPeer) Exec(t *testing.T, sql string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := g.c.Query(ctx, sql); err != nil {
		t.Fatalf("goopg exec %q: %v", sql, err)
	}
}
func (g *goopgPeer) QueryScalar(t *testing.T, sql string) string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	rows, err := g.c.Query(ctx, sql)
	if err != nil {
		t.Fatalf("goopg query %q: %v", sql, err)
	}
	if len(rows) == 0 || len(rows[0]) == 0 {
		return ""
	}
	return rows[0][0]
}

// pgPeer adapts `*pgcluster.Cluster` (upstream PG) to ReplPeer.
type pgPeer struct {
	c *pgcluster.Cluster
}

func (p *pgPeer) Kind() ClusterKind                           { return ClusterKindPG }
func (p *pgPeer) Host() string                                { return p.c.Host() }
func (p *pgPeer) Port() int                                   { return p.c.Port() }
func (p *pgPeer) User() string                                { return p.c.User() }
func (p *pgPeer) Database() string                            { return p.c.Database() }
func (p *pgPeer) Conninfo(applicationName string) string      { return p.c.Conninfo(applicationName) }
func (p *pgPeer) Start() error                                { return p.c.Start() }
func (p *pgPeer) Stop() error                                 { return p.c.Stop() }
func (p *pgPeer) Exec(t *testing.T, sql string)               { p.c.Exec(t, sql) }
func (p *pgPeer) QueryScalar(t *testing.T, sql string) string { return p.c.QueryScalar(t, sql) }

func splitHostPort(addr string) (string, string, error) {
	idx := strings.LastIndex(addr, ":")
	if idx < 0 {
		return "", "", fmt.Errorf("missing port in %q", addr)
	}
	return addr[:idx], addr[idx+1:], nil
}

func atoi(s string) (int, error) {
	n := 0
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < '0' || c > '9' {
			return 0, fmt.Errorf("not a number: %q", s)
		}
		n = n*10 + int(c-'0')
	}
	return n, nil
}
