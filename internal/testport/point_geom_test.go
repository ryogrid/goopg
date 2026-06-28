package testport

import (
	"testing"

	"github.com/goopg/goopg/internal/testutil/cluster"
)

// TestPort_PointGeometricRead exercises end-to-end (analyzer + planner +
// executor) point coordinate subscripting and the strictly-left/right
// geometric operators required by the predicate-gist isolation spec
// (M0118-0002): `point[0]`/`point[1]` return the X/Y coordinate as float8
// (0-based, PG geometric semantics), and `p << q` / `p >> q` compare X
// coordinates yielding bool.
func TestPort_PointGeometricRead(t *testing.T) {
	c := newCluster(t, "point_geom_read")
	mustInitStart(t, c)
	defer func() { _ = c.Stop(cluster.ShutdownImmediate) }()

	runSQL(t, c, "CREATE TABLE gp (id int4, p point)")
	runSQL(t, c, "INSERT INTO gp (id, p) SELECT g, point(g*10, g*20) FROM generate_series(1,5) g")

	// point[0] = X, point[1] = Y (0-based).
	rows := runSQL(t, c, "SELECT p[0], p[1] FROM gp WHERE id = 3")
	if len(rows) != 1 || rows[0][0] != "30" || rows[0][1] != "60" {
		t.Fatalf("p[0],p[1] for id=3: got %v want [30 60]", rows)
	}

	// sum(point[0]) must type-check (subscript is numeric-like) and aggregate.
	rows = runSQL(t, c, "SELECT sum(p[0]) FROM gp") // 10+20+30+40+50 = 150
	if len(rows) != 1 || rows[0][0] != "150" {
		t.Fatalf("sum(p[0]): got %v want [150]", rows)
	}

	// Strictly-left-of / strictly-right-of compare X coordinates.
	rows = runSQL(t, c, "SELECT id FROM gp WHERE p << point(35,0) ORDER BY id") // X < 35 -> ids 1,2,3
	if len(rows) != 3 {
		t.Fatalf("p << point(35,0): got %v want 3 rows", rows)
	}
	rows = runSQL(t, c, "SELECT id FROM gp WHERE p >> point(35,0) ORDER BY id") // X > 35 -> ids 4,5
	if len(rows) != 2 {
		t.Fatalf("p >> point(35,0): got %v want 2 rows", rows)
	}

	_ = cluster.ShutdownImmediate
}
