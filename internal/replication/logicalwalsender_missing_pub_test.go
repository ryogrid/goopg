package replication

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/goopg/goopg/internal/access/transam/xlog"
	"github.com/goopg/goopg/internal/catalog"
)

// TestLivePublicationFilterWarnsMissingPublication covers pgoutput's
// LoadPublications (M0149-0052a, subscription/t/024_add_drop_pub.pl): the
// publications load on the first change consulted, a requested name with
// no publication logs WARNING "skipped loading publication" and is skipped,
// and the filter picks the publication up once it is created.
func TestLivePublicationFilterWarnsMissingPublication(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	ps := catalog.NewPubSub()
	f := newLivePublicationFilter(ps, []string{"tap_pub_3"})
	if buf.Len() != 0 {
		t.Fatalf("warned before any change was decoded: %s", buf.String())
	}
	tab3 := &xlog.RelationDef{Schema: "public", Name: "tab_3"}
	if f.Allows(tab3, xlog.ChangeInsert) {
		t.Fatal("tab_3 streamed with no publication")
	}
	if got := strings.Count(buf.String(), `skipped loading publication \"tap_pub_3\"`); got != 1 || !strings.Contains(buf.String(), "level=WARN") {
		t.Fatalf("want one WARN for tap_pub_3, got %d in %s", got, buf.String())
	}
	// No reload, no second warning.
	f.Allows(tab3, xlog.ChangeInsert)
	if got := strings.Count(buf.String(), "skipped loading publication"); got != 1 {
		t.Fatalf("warned %d times without a registry change", got)
	}
	if _, err := ps.CreatePublication("tap_pub_3", []string{"public.tab_3"}, catalog.DefaultPublicationOptions()); err != nil {
		t.Fatal(err)
	}
	if !f.Allows(tab3, xlog.ChangeInsert) {
		t.Fatal("tab_3 not streamed after its publication was created")
	}
	if got := strings.Count(buf.String(), "skipped loading publication"); got != 1 {
		t.Fatalf("warned again after the publication was created (%d)", got)
	}
}
