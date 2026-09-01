package postmaster

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/libpq"
)

// TestShowAllEmitsDescriptionColumn pins review/260831-2 EO2-8: PG's
// `SHOW ALL` is `name, setting, description` (guc.c ShowAllGUCConfig), but
// goopg emitted only the first two columns, so a client reading the third
// by index — psql's own \dconfig-style consumers included — got a short
// row. The description text comes from pg_settings.short_desc.
func TestShowAllEmitsDescriptionColumn(t *testing.T) {
	addr, stop := startTestServer(t)
	defer stop()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeQuery(t, conn, "SHOW ALL")
	frames := readUntilReady(t, conn)
	if frames[0].Type != libpq.MsgRowDescription {
		t.Fatalf("frame[0] = %c, want T (RowDescription)", frames[0].Type)
	}
	names := rowDescriptionNames(t, frames[0].Payload)
	want := []string{"name", "setting", "description"}
	if len(names) != len(want) {
		t.Fatalf("SHOW ALL RowDescription has %d columns (%v), want %d (%v)", len(names), names, len(want), want)
	}
	for i, w := range want {
		if names[i] != w {
			t.Errorf("column[%d] = %q, want %q", i, names[i], w)
		}
	}
	for _, f := range frames {
		if f.Type != libpq.MsgDataRow {
			continue
		}
		if n := int(f.Payload[0])<<8 | int(f.Payload[1]); n != 3 {
			t.Fatalf("DataRow has %d columns, want 3", n)
		}
	}
}

// rowDescriptionNames pulls the field names out of a RowDescription
// payload: int16 nfields, then per field a cstring name followed by 18
// bytes of table/type metadata.
func rowDescriptionNames(t *testing.T, p []byte) []string {
	t.Helper()
	n := int(p[0])<<8 | int(p[1])
	out := make([]string, 0, n)
	off := 2
	for i := 0; i < n; i++ {
		end := bytes.IndexByte(p[off:], 0)
		if end < 0 {
			t.Fatalf("RowDescription field %d has no NUL terminator", i)
		}
		out = append(out, string(p[off:off+end]))
		off += end + 1 + 18
	}
	return out
}

// TestShowAllDescriptionIsPopulated pins the other half of EO2-8: the
// description column must actually carry pg_settings.short_desc, not a
// blank placeholder, for every GUC that view serves. startTestServer runs
// without a catalog, so this one wires one in.
func TestShowAllDescriptionIsPopulated(t *testing.T) {
	srv := New(Config{
		Address:          "127.0.0.1:0",
		Logger:           slog.New(slog.NewTextHandler(io.Discard, nil)),
		AcceptDeadline:   25 * time.Millisecond,
		HandshakeTimeout: 2 * time.Second,
		Catalog:          catalog.NewInMemory(),
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()
	<-srv.Ready()
	addr := srv.Addr().String()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Error("Server.Run did not return within 2s of cancel")
		}
	}()

	conn := dialAndComplete(t, addr)
	defer conn.Close()
	writeQuery(t, conn, "SHOW ALL")

	const guc = "enable_seqscan"
	want := "Enables the planner's use of sequential-scan plans."
	found := false
	for _, f := range readUntilReady(t, conn) {
		if f.Type != libpq.MsgDataRow || !bytes.Contains(f.Payload, []byte(guc)) {
			continue
		}
		cols := dataRowColumns(t, f.Payload)
		if cols[0] != guc {
			continue
		}
		found = true
		if cols[2] != want {
			t.Errorf("%s description = %q, want %q", guc, cols[2], want)
		}
	}
	if !found {
		t.Fatalf("SHOW ALL did not include %s", guc)
	}
}

// dataRowColumns splits a DataRow payload into its column values (NULLs,
// which SHOW ALL never emits, come back as empty strings).
func dataRowColumns(t *testing.T, p []byte) []string {
	t.Helper()
	n := int(p[0])<<8 | int(p[1])
	out := make([]string, 0, n)
	off := 2
	for i := 0; i < n; i++ {
		l := int(int32(uint32(p[off])<<24 | uint32(p[off+1])<<16 | uint32(p[off+2])<<8 | uint32(p[off+3])))
		off += 4
		if l < 0 {
			out = append(out, "")
			continue
		}
		out = append(out, string(p[off:off+l]))
		off += l
	}
	return out
}
