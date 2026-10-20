package postmaster

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/initdb"
)

// TestCtidSurvivesWindowAgg pins M0146-0236: `ctid` selected beside a window
// function. PG computes the system-column Var below the WindowAgg and passes
// it up as a column (make_window_input_target), so every output row carries
// its own source TID. goopg's ctid rides the slot's TID side-channel, and the
// window operator — which buffers and re-sorts its input — dropped it, so
// `ctid` read NULL on every row.
func TestCtidSurvivesWindowAgg(t *testing.T) {
	dir := t.TempDir()
	if err := initdb.Init(initdb.Options{DataDir: dir}); err != nil {
		t.Fatalf("initdb.Init: %v", err)
	}
	s := startVMRestartServer(t, dir)
	defer s.close(t)
	db := s.open(t, "postgres")
	defer db.Close()
	ctx := context.Background()
	for _, stmt := range []string{
		"CREATE TABLE wc (id int, k int)",
		"INSERT INTO wc SELECT g, g % 3 FROM generate_series(1, 6) g",
	} {
		if _, err := db.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}
	// Each row's ctid is (0,id): six rows inserted in id order on one page.
	for q, want := range map[string]string{
		"SELECT ctid, row_number() OVER () FROM wc":                                 "(0,1)/1 (0,2)/2 (0,3)/3 (0,4)/4 (0,5)/5 (0,6)/6",
		"SELECT ctid, row_number() OVER (ORDER BY id DESC) FROM wc":                 "(0,6)/1 (0,5)/2 (0,4)/3 (0,3)/4 (0,2)/5 (0,1)/6",
		"SELECT ctid, rank() OVER (PARTITION BY k ORDER BY id) FROM wc ORDER BY id": "(0,1)/1 (0,2)/1 (0,3)/1 (0,4)/2 (0,5)/2 (0,6)/2",
	} {
		rows, err := db.QueryContext(ctx, q)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		var got []string
		for rows.Next() {
			var tid *string
			var n int64
			if err := rows.Scan(&tid, &n); err != nil {
				t.Fatalf("%s: scan: %v", q, err)
			}
			v := "NULL"
			if tid != nil {
				v = *tid
			}
			got = append(got, v+"/"+strconv.FormatInt(n, 10))
		}
		rows.Close()
		if g := strings.Join(got, " "); g != want {
			t.Errorf("%s:\n got %s\nwant %s", q, g, want)
		}
	}
}
