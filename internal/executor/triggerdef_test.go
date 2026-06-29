package executor

import (
	"testing"

	"github.com/goopg/goopg/internal/catalog"
)

// TestBuildTriggerDefString verifies pg_get_triggerdef reconstruction matches
// the spacing and clause order produced by ruleutils.c
// pg_get_triggerdef_worker. pg_dump emits the result verbatim, so any spacing
// drift would corrupt the dumped CREATE TRIGGER. DU-002 slice 319.
func TestBuildTriggerDefString(t *testing.T) {
	tbl := &catalog.Table{Schema: "public", Name: "t", OID: 16500}
	cases := []struct {
		name string
		trig catalog.Trigger
		want string
	}{
		{
			name: "before insert for each row",
			trig: catalog.Trigger{
				Name: "trg_ins", Timing: catalog.TriggerBefore,
				Events: []string{"insert"}, ForEachRow: true,
				FuncSchema: "public", FuncName: "f",
			},
			want: "CREATE TRIGGER trg_ins BEFORE INSERT ON public.t FOR EACH ROW EXECUTE FUNCTION public.f()",
		},
		{
			name: "after insert or update statement-level",
			trig: catalog.Trigger{
				Name: "trg_iu", Timing: catalog.TriggerAfter,
				Events: []string{"update", "insert"}, ForEachRow: false,
				FuncSchema: "public", FuncName: "g",
			},
			// PG canonical event order is INSERT then UPDATE regardless of
			// declaration order.
			want: "CREATE TRIGGER trg_iu AFTER INSERT OR UPDATE ON public.t FOR EACH STATEMENT EXECUTE FUNCTION public.g()",
		},
		{
			name: "all four events with args",
			trig: catalog.Trigger{
				Name: "trg_all", Timing: catalog.TriggerAfter,
				Events: []string{"insert", "update", "delete", "truncate"}, ForEachRow: true,
				FuncSchema: "public", FuncName: "h", Args: []string{"a", "b'c"},
			},
			want: "CREATE TRIGGER trg_all AFTER INSERT OR DELETE OR UPDATE OR TRUNCATE ON public.t FOR EACH ROW EXECUTE FUNCTION public.h('a', 'b''c')",
		},
		{
			name: "instead of with empty func schema defaults to public",
			trig: catalog.Trigger{
				Name: "trg_io", Timing: catalog.TriggerInsteadOf,
				Events: []string{"delete"}, ForEachRow: true,
				FuncName: "v",
			},
			want: "CREATE TRIGGER trg_io INSTEAD OF DELETE ON public.t FOR EACH ROW EXECUTE FUNCTION public.v()",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildTriggerDefString(tbl, tc.trig)
			if got != tc.want {
				t.Fatalf("buildTriggerDefString mismatch:\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}
