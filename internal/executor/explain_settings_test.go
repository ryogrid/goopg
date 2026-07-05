package executor

import (
	"strings"
	"testing"
)

// stubExplainSettings wires ctx.ExplainSettings to a fixed list, mirroring
// the shape SessionRegistry.ExplainVariables/dispatch.go would produce
// without needing a full config.SessionRegistry in these unit tests.
func stubExplainSettings(ctx *Context, vals []SettingValue) {
	ctx.ExplainSettings = func() []SettingValue { return vals }
}

// TestExplainSettingsTextOmittedByDefault: EXPLAIN without SETTINGS never
// renders a "Settings:" line, even when modified GUCs are wired up —
// SETTINGS defaults to FALSE upstream (explain.sgml).
func TestExplainSettingsTextOmittedByDefault(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	stubExplainSettings(ctx, []SettingValue{{Name: "enable_seqscan", Value: "off"}})

	lines := runExplainRows(t, ctx, "EXPLAIN SELECT 1")
	for _, l := range lines {
		if strings.HasPrefix(l, "Settings:") {
			t.Fatalf("unexpected Settings line without SETTINGS option:\n%s", strings.Join(lines, "\n"))
		}
	}
}

// TestExplainSettingsTextListsModifiedGUCs: `EXPLAIN (SETTINGS)` renders a
// single "Settings: name = 'value', ..." line, matching upstream's
// ExplainPrintSettings TEXT-format branch (explain.c).
func TestExplainSettingsTextListsModifiedGUCs(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	stubExplainSettings(ctx, []SettingValue{
		{Name: "enable_seqscan", Value: "off"},
		{Name: "work_mem", Value: "1048576"},
	})

	lines := runExplainRows(t, ctx, "EXPLAIN (SETTINGS) SELECT 1")
	last := lines[len(lines)-1]
	want := "Settings: enable_seqscan = 'off', work_mem = '1048576'"
	if last != want {
		t.Fatalf("Settings line = %q, want %q\nfull output:\n%s", last, want, strings.Join(lines, "\n"))
	}
}

// TestExplainSettingsTextOmittedWhenNoneModified: with SETTINGS requested
// but zero modified GUCs, TEXT format prints nothing at all — upstream's
// ExplainPrintSettings bails out early ("if (num <= 0) return;") rather than
// emitting an empty "Settings:" label.
func TestExplainSettingsTextOmittedWhenNoneModified(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	stubExplainSettings(ctx, nil)

	lines := runExplainRows(t, ctx, "EXPLAIN (SETTINGS) SELECT 1")
	for _, l := range lines {
		if strings.HasPrefix(l, "Settings:") {
			t.Fatalf("unexpected Settings line with zero modified GUCs:\n%s", strings.Join(lines, "\n"))
		}
	}
}

// TestExplainSettingsJSONAlwaysIncludesGroup: FORMAT JSON emits a "Settings"
// object once SETTINGS is requested, even when it is empty — unlike TEXT,
// upstream's structured-format branch has no num<=0 early return.
func TestExplainSettingsJSONAlwaysIncludesGroup(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	stubExplainSettings(ctx, nil)

	lines := runExplainRows(t, ctx, "EXPLAIN (SETTINGS, FORMAT JSON) SELECT 1")
	out := lines[0]
	if !strings.Contains(out, `"Settings": {}`) {
		t.Fatalf("expected empty Settings object in JSON output:\n%s", out)
	}
}

// TestExplainSettingsJSONListsModifiedGUCs: FORMAT JSON's "Settings" object
// carries the modified GUC name/value pairs as sibling keys of "Plan".
func TestExplainSettingsJSONListsModifiedGUCs(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	stubExplainSettings(ctx, []SettingValue{{Name: "enable_seqscan", Value: "off"}})

	lines := runExplainRows(t, ctx, "EXPLAIN (SETTINGS, FORMAT JSON) SELECT 1")
	out := lines[0]
	if !strings.Contains(out, `"Settings"`) || !strings.Contains(out, `"enable_seqscan": "off"`) {
		t.Fatalf("expected Settings.enable_seqscan in JSON output:\n%s", out)
	}
}

// TestExplainSettingsAnalyzeTextPlacement: under ANALYZE, the Settings line
// is emitted after the plan tree but before Planning Time / Execution Time,
// mirroring ExplainPrintSettings's placement inside ExplainPrintPlan (called
// before the caller appends the timing summary lines).
func TestExplainSettingsAnalyzeTextPlacement(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	stubExplainSettings(ctx, []SettingValue{{Name: "enable_seqscan", Value: "off"}})

	lines := runExplainRows(t, ctx, "EXPLAIN (ANALYZE, SETTINGS) SELECT 1")
	if len(lines) < 3 {
		t.Fatalf("expected at least 3 rows (plan, settings, planning/execution time), got %d:\n%s",
			len(lines), strings.Join(lines, "\n"))
	}
	settingsIdx := -1
	for i, l := range lines {
		if strings.HasPrefix(l, "Settings:") {
			settingsIdx = i
		}
	}
	if settingsIdx == -1 {
		t.Fatalf("no Settings line found:\n%s", strings.Join(lines, "\n"))
	}
	if !strings.HasPrefix(lines[settingsIdx+1], "Planning Time:") {
		t.Fatalf("Settings line not immediately before Planning Time:\n%s", strings.Join(lines, "\n"))
	}
}
