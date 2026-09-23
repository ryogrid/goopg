package postmaster

import (
	"testing"

	"github.com/goopg/goopg/internal/executor"
	"github.com/goopg/goopg/internal/libpq"
)

// TestExecutorNoticeFrames: the one builder both wire paths use to turn the
// executor's queued messages into NoticeResponse frames. The extended
// protocol used to drop every operator-raised NOTICE and WARNING; it now
// drains the Context through this function exactly as the simple path does.
func TestExecutorNoticeFrames(t *testing.T) {
	ctx := executor.NewContext()
	ctx.AddNotice("n1")
	ctx.AddNoticeWithDetail("n2", "d2")
	ctx.AddWarning("w1")
	ctx.AddWarningWithHint("55000", "w2", "h2")
	frames := executorNoticeFrames(ctx)
	if len(frames) != 4 {
		t.Fatalf("%d frames, want 4", len(frames))
	}
	field := func(f []libpq.ErrorField, code byte) string {
		for _, e := range f {
			if e.Code == code {
				return e.Value
			}
		}
		return ""
	}
	for i, want := range []struct{ sev, state, msg, detail, hint string }{
		{"NOTICE", "00000", "n1", "", ""},
		{"NOTICE", "00000", "n2", "d2", ""},
		{"WARNING", "55000", "w1", "", ""},
		{"WARNING", "55000", "w2", "", "h2"},
	} {
		f := frames[i]
		if field(f, libpq.FieldSeverity) != want.sev || field(f, libpq.FieldSQLState) != want.state ||
			field(f, libpq.FieldMessage) != want.msg || field(f, libpq.FieldDetail) != want.detail ||
			field(f, libpq.FieldHint) != want.hint {
			t.Errorf("frame %d = %+v, want %+v", i, f, want)
		}
	}
	if again := executorNoticeFrames(ctx); len(again) != 0 {
		t.Errorf("queues not drained: %d frames on the second call", len(again))
	}
}
