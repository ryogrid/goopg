package server

import (
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/protocol"
)

// M0119-0006 (45th slice). `COPY … FROM` used to ignore FORMAT csv
// entirely — the line was split on TAB as COPY TEXT, so even an
// unquoted `1,alpha` into a two-column table failed with
// `COPY: row has 1 fields, expected 2`. These tests drive the real
// executor path (startCopyExecServer, table `items(id int4, label text)`);
// startTestServer cannot reach the decoder at all (it is storage-less and
// row-counts instead — see the 44th slice's ledger row).
//
// Expectations come from the PG 18.3 oracle on port 65432 (2026-08-13).

// copyCsvIn runs one COPY FROM STDIN with the given SQL and payload and
// returns the frame types plus the CommandComplete tag (or the error
// message when the server reported one).
func copyCsvIn(t *testing.T, addr, sql, payload string) (frames string, tag string) {
	t.Helper()
	conn := dialAndComplete(t, addr)
	defer conn.Close()

	writeQuery(t, conn, sql)
	writeFrontendFrame(t, conn, protocol.MsgCopyData, []byte(payload))
	writeFrontendFrame(t, conn, protocol.MsgCopyDone, nil)

	var b strings.Builder
	for _, f := range readUntilReady(t, conn) {
		b.WriteByte(f.Type)
		switch f.Type {
		case protocol.MsgCommandComplete:
			tag = strings.TrimSuffix(string(f.Payload), "\x00")
		case protocol.MsgErrorResponse:
			tag = errorFieldValue(f.Payload, protocol.FieldMessage)
		}
	}
	return b.String(), tag
}

// errorFieldValue pulls one field out of an ErrorResponse payload.
func errorFieldValue(payload []byte, code byte) string {
	for i := 0; i < len(payload); {
		c := payload[i]
		if c == 0 {
			break
		}
		i++
		end := i
		for end < len(payload) && payload[end] != 0 {
			end++
		}
		if c == code {
			return string(payload[i:end])
		}
		i = end + 1
	}
	return ""
}

// TestCopyFromCsvBasic: a CSV stream is split on the CSV delimiter, and
// quoting decides NULL — an unquoted empty field is NULL while a quoted
// one is the empty string.
func TestCopyFromCsvBasic(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()

	frames, tag := copyCsvIn(t, addr,
		"COPY items FROM STDIN WITH (FORMAT csv)",
		"1,plain\n2,\"has,comma\"\n3,\"dbl\"\"quote\"\n4,\n5,\"\"\n")
	if tag != "COPY 5" {
		t.Fatalf("tag=%q frames=%q want COPY 5", tag, frames)
	}

	conn := dialAndComplete(t, addr)
	defer conn.Close()
	writeQuery(t, conn, "COPY items TO STDOUT")
	var out strings.Builder
	for _, f := range readUntilReady(t, conn) {
		if f.Type == protocol.MsgCopyData {
			out.Write(f.Payload)
		}
	}
	want := "1\tplain\n2\thas,comma\n3\tdbl\"quote\n4\t\\N\n5\t\n"
	if out.String() != want {
		t.Fatalf("round trip = %q\nwant           %q", out.String(), want)
	}
}

// TestCopyFromCsvEmbeddedNewline: a quoted field may contain the record
// terminator. The wire layer splits CopyData on '\n', so this only works
// if the reader re-joins the halves and restores the newline.
func TestCopyFromCsvEmbeddedNewline(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()

	frames, tag := copyCsvIn(t, addr,
		"COPY items FROM STDIN WITH (FORMAT csv)",
		"1,\"embedded\nnewline\"\n2,after\n")
	if tag != "COPY 2" {
		t.Fatalf("tag=%q frames=%q want COPY 2", tag, frames)
	}

	conn := dialAndComplete(t, addr)
	defer conn.Close()
	writeQuery(t, conn, "COPY items TO STDOUT")
	var out strings.Builder
	for _, f := range readUntilReady(t, conn) {
		if f.Type == protocol.MsgCopyData {
			out.Write(f.Payload)
		}
	}
	// COPY TEXT output escapes the embedded newline as \n.
	want := "1\tembedded\\nnewline\n2\tafter\n"
	if out.String() != want {
		t.Fatalf("round trip = %q want %q", out.String(), want)
	}
}

// TestCopyFromCsvHeaderSkipped: HEADER discards the first line.
func TestCopyFromCsvHeaderSkipped(t *testing.T) {
	addr, _, stop := startCopyExecServer(t)
	defer stop()

	if frames, tag := copyCsvIn(t, addr,
		"COPY items FROM STDIN WITH (FORMAT csv, HEADER)",
		"id,label\n1,one\n"); tag != "COPY 1" {
		t.Fatalf("tag=%q frames=%q want COPY 1", tag, frames)
	}
}

// TestCopyFromCsvErrors: the two field-count mismatches and the
// unterminated quoted field carry upstream's messages. The `\.` line in
// the last case is DATA (it is inside the open quote), which is why the
// error is the unterminated-field one rather than a silent success.
func TestCopyFromCsvErrors(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    string
	}{
		{"extra column", "1,a,b\n", "extra data after last expected column"},
		{"missing column", "1\n", `missing data for column "label"`},
		{"unterminated quote", "1,\"open\n\\.\n", "unterminated CSV quoted field"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			addr, _, stop := startCopyExecServer(t)
			defer stop()
			frames, msg := copyCsvIn(t, addr,
				"COPY items FROM STDIN WITH (FORMAT csv)", tc.payload)
			if !strings.Contains(frames, string(protocol.MsgErrorResponse)) {
				t.Fatalf("frames=%q want an ErrorResponse (msg=%q)", frames, msg)
			}
			if !strings.Contains(msg, tc.want) {
				t.Fatalf("message=%q want it to contain %q", msg, tc.want)
			}
		})
	}
}
