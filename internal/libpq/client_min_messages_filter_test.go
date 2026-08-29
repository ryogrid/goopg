package libpq

import (
	"bytes"
	"testing"
)

// M0134-0165: goopg defined the client_min_messages GUC but nothing consumed
// it, so every NOTICE/WARNING reached the client no matter what the session
// had set. `SET client_min_messages TO 'warning'` — the first two lines of
// upstream's security_label.sql, and of 14 other regress cases — therefore
// still let `DROP ROLE IF EXISTS` print its "does not exist, skipping" NOTICE.
//
// These tests pin the filter against should_output_to_client
// (postgres/src/backend/utils/error/elog.c):
//
//	return (elevel >= client_min_messages || elevel == INFO);
//
// with the elevels of postgres/src/include/utils/elog.h and the GUC values of
// client_message_level_options (guc_tables.c).

func TestShouldOutputToClientMatchesUpstream(t *testing.T) {
	tests := []struct {
		severity  string
		clientMin string
		want      bool
		why       string
	}{
		// Default GUC ("notice"): NOTICE and above are visible. This row is
		// the non-regression anchor — the overwhelming majority of goopg
		// connections never touch the GUC.
		{"NOTICE", "notice", true, "NOTICE(18) >= notice(18)"},
		{"WARNING", "notice", true, "WARNING(19) >= notice(18)"},

		// The security_label.sql setting: NOTICE suppressed, WARNING kept.
		{"NOTICE", "warning", false, "NOTICE(18) < warning(19)"},
		{"WARNING", "warning", true, "WARNING(19) >= warning(19)"},

		// "error" suppresses both.
		{"NOTICE", "error", false, "NOTICE(18) < error(21)"},
		{"WARNING", "error", false, "WARNING(19) < error(21)"},

		// A debug setting lets everything through.
		{"NOTICE", "debug1", true, "NOTICE(18) >= debug1(14)"},
		{"WARNING", "debug5", true, "WARNING(19) >= debug5(10)"},

		// INFO is unconditional upstream: "always sent to client regardless
		// of client_min_messages" (elog.h). This is the one row a naive
		// `elevel >= min` comparison gets wrong.
		{"INFO", "error", true, "INFO is exempt from the comparison"},
		{"INFO", "warning", true, "INFO is exempt from the comparison"},

		// LOG is below NOTICE, so the default GUC hides it.
		{"LOG", "notice", false, "LOG(15) < notice(18)"},
		{"LOG", "log", true, "LOG(15) >= log(15)"},

		// ERROR/FATAL/PANIC clear even the highest setting — which is why
		// WriteErrorResponse carries no gate at all.
		{"ERROR", "error", true, "ERROR(21) >= error(21)"},
		{"FATAL", "error", true, "FATAL(22) >= error(21)"},
		{"PANIC", "error", true, "PANIC(23) >= error(21)"},

		// Upstream's hidden aliases are accepted as GUC input.
		{"NOTICE", "info", true, "NOTICE(18) >= info(17)"},
		{"NOTICE", "debug", true, "debug is an alias for debug2(13)"},

		// Case-insensitive GUC lookup, as config_enum_lookup_by_name is.
		{"NOTICE", "WARNING", false, "GUC value compared case-insensitively"},

		// Fail open on anything unclassifiable rather than silently dropping
		// a message.
		{"NOTICE", "", true, "empty GUC value is not classifiable"},
		{"NOTICE", "bogus", true, "unknown GUC value is not classifiable"},
		{"SURPRISE", "error", true, "unknown severity is not classifiable"},
	}
	for _, tt := range tests {
		if got := ShouldOutputToClient(tt.severity, tt.clientMin); got != tt.want {
			t.Errorf("ShouldOutputToClient(%q, %q) = %v, want %v (%s)",
				tt.severity, tt.clientMin, got, tt.want, tt.why)
		}
	}
}

// TestWriteNoticeResponseHonorsClientMinMessages exercises the choke point
// itself: the filter must run inside WriteNoticeResponse so that no producer
// can bypass it, and it must read the hook on every call so a mid-session
// SET takes effect immediately.
func TestWriteNoticeResponseHonorsClientMinMessages(t *testing.T) {
	notice := []ErrorField{
		{Code: FieldSeverity, Value: "NOTICE"},
		{Code: FieldSeverityNonLocal, Value: "NOTICE"},
		{Code: FieldSQLState, Value: "00000"},
		{Code: FieldMessage, Value: `role "r" does not exist, skipping`},
	}
	warning := []ErrorField{
		{Code: FieldSeverity, Value: "WARNING"},
		{Code: FieldSeverityNonLocal, Value: "WARNING"},
		{Code: FieldSQLState, Value: "55000"},
		{Code: FieldMessage, Value: "there is no transaction in progress"},
	}

	// A nil hook (pre-startup, and every non-server FrameWriter) sends
	// everything — the pre-change behaviour.
	var buf bytes.Buffer
	w := NewFrameWriter(&buf)
	if err := w.WriteNoticeResponse(notice); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if buf.Len() == 0 {
		t.Fatal("nil ClientMinMessagesFn suppressed a NOTICE; it must fail open")
	}

	// With the hook installed, the GUC decides. Reading it live is what makes
	// SET/SET LOCAL/RESET take effect mid-session.
	buf.Reset()
	clientMin := "warning"
	w = NewFrameWriter(&buf)
	w.ClientMinMessagesFn = func() string { return clientMin }

	if err := w.WriteNoticeResponse(notice); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if buf.Len() != 0 {
		t.Errorf("NOTICE reached the wire under client_min_messages=warning: % x", buf.Bytes())
	}

	// Same writer, same call: WARNING still gets through.
	if err := w.WriteNoticeResponse(warning); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if buf.Len() == 0 {
		t.Fatal("WARNING was suppressed under client_min_messages=warning")
	}
	if got := buf.Bytes()[0]; got != MsgNoticeResponse {
		t.Errorf("first byte = %q, want %q (NoticeResponse)", got, MsgNoticeResponse)
	}

	// A RESET back to the default un-suppresses the NOTICE without rebuilding
	// the FrameWriter.
	buf.Reset()
	clientMin = "notice"
	if err := w.WriteNoticeResponse(notice); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if buf.Len() == 0 {
		t.Error("NOTICE still suppressed after client_min_messages returned to notice")
	}
}

// TestWriteErrorResponseIsNeverFiltered pins the deliberate asymmetry: the
// gate is on notices only. ERROR is elevel 21 and client_min_messages caps at
// "error" (21), so upstream's comparison admits every ErrorResponse — adding a
// gate there could only ever swallow an error.
func TestWriteErrorResponseIsNeverFiltered(t *testing.T) {
	var buf bytes.Buffer
	w := NewFrameWriter(&buf)
	w.ClientMinMessagesFn = func() string { return "error" }
	if err := w.WriteErrorResponse([]ErrorField{
		{Code: FieldSeverity, Value: "ERROR"},
		{Code: FieldSeverityNonLocal, Value: "ERROR"},
		{Code: FieldSQLState, Value: "42P01"},
		{Code: FieldMessage, Value: `relation "t" does not exist`},
	}); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}
	if buf.Len() == 0 {
		t.Fatal("ErrorResponse was filtered; WriteErrorResponse must carry no gate")
	}
	if got := buf.Bytes()[0]; got != MsgErrorResponse {
		t.Errorf("first byte = %q, want %q (ErrorResponse)", got, MsgErrorResponse)
	}
}

// TestNoticeSeverityPrefersNonLocalField pins which field the filter keys on.
// 'V' is the non-localized severity whose spelling the protocol fixes; 'S' may
// be translated. Keying on 'S' would make the filter locale-dependent.
func TestNoticeSeverityPrefersNonLocalField(t *testing.T) {
	if got := noticeSeverity([]ErrorField{
		{Code: FieldSeverity, Value: "HINWEIS"}, // a translated 'S'
		{Code: FieldSeverityNonLocal, Value: "NOTICE"},
	}); got != "NOTICE" {
		t.Errorf("noticeSeverity = %q, want NOTICE (the 'V' field)", got)
	}
	// Field sets that predate 'V' still classify off 'S'.
	if got := noticeSeverity([]ErrorField{
		{Code: FieldSeverity, Value: "NOTICE"},
	}); got != "NOTICE" {
		t.Errorf("noticeSeverity = %q, want NOTICE (the 'S' fallback)", got)
	}
}
