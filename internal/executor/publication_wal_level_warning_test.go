package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestCreatePublicationWalLevelWarning pins CreatePublication's WARNING
// (publicationcmds.c): below wal_level = logical the publication is still
// created, with 55000 `"wal_level" is insufficient to publish logical
// changes` and its HINT; at logical there is no warning. Found by the
// 2026-09-23 command-tag sweep; the wire text was matched against PG 18.3.
func TestCreatePublicationWalLevelWarning(t *testing.T) {
	ctx, _, cleanup := newDDLFixture(t)
	defer cleanup()
	ctx.PubSub = catalog.NewPubSub()
	level := "replica"
	ctx.GetSetting = func(name string) (string, bool) {
		if name == "wal_level" {
			return level, true
		}
		return "", false
	}
	runSQL(t, ctx, "CREATE PUBLICATION pw1 FOR ALL TABLES")
	got := ctx.TakeWarningsWithHint()
	if len(got) != 1 || got[0].Code != "55000" ||
		got[0].Message != `"wal_level" is insufficient to publish logical changes` ||
		got[0].Hint != `Set "wal_level" to "logical" before creating subscriptions.` {
		t.Fatalf("wal_level=replica: warnings %+v, want PG's one warning with hint", got)
	}
	level = "logical"
	runSQL(t, ctx, "CREATE PUBLICATION pw2 FOR ALL TABLES")
	if got := ctx.TakeWarningsWithHint(); len(got) != 0 {
		t.Fatalf("wal_level=logical: warnings %+v, want none", got)
	}
}
