package postmaster

import (
	"io"
	"log/slog"
	"testing"

	"github.com/goopg/goopg/internal/catalog"
	"github.com/goopg/goopg/internal/executor"
)

// TestAppendTypedCellTextXid8RendersUnsigned pins M0134-0087 (xid.sql
// sizing): xid8 is a 64-bit UNSIGNED transaction ID (xid8out, postgres/src/
// backend/utils/adt/xid.c), but goopg's Datum.Int carries it as a signed
// int64 — a value like 2^64-1 stores as int64(-1). appendTypedCellText's
// default arm (executor.Datum.AppendValueText) does a plain SIGNED
// strconv.AppendInt and rendered such a value as "-1" over the TEXT wire
// protocol instead of "18446744073709551615", even though the binary/COPY
// encoders already got this right via pgUnsignedIDFromDatum
// (internal/executor/codec.go). appendTypedCellText is shared by the
// simple-query and extended-query text paths, so this pins both.
func TestAppendTypedCellTextXid8RendersUnsigned(t *testing.T) {
	srv := New(Config{
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
		Catalog: catalog.NewInMemory(),
	})
	xid8Type := catalog.Type{Name: "xid8"}

	cases := []struct {
		name string
		in   int64
		want string
	}{
		{"zero", 0, "0"},
		{"small positive", 42, "42"},
		{"uint64 max (bit pattern -1)", -1, "18446744073709551615"},
		{"large near max", -1040, "18446744073709550576"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := executor.Datum{Kind: executor.KindInt, Int: tc.in}
			got := string(srv.appendTypedCellText(nil, d, xid8Type, nil))
			if got != tc.want {
				t.Errorf("appendTypedCellText(xid8, Int=%d) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
