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
			// DU-002 slice 326: a column-specific UPDATE trigger appends
			// ` OF col1, col2` right after the UPDATE event.
			name: "update of columns",
			trig: catalog.Trigger{
				Name: "trg_uof", Timing: catalog.TriggerBefore,
				Events: []string{"update"}, UpdateColumns: []string{"a", "b"},
				ForEachRow: true, FuncSchema: "public", FuncName: "f",
			},
			want: "CREATE TRIGGER trg_uof BEFORE UPDATE OF a, b ON public.t FOR EACH ROW EXECUTE FUNCTION public.f()",
		},
		{
			// The OF clause attaches to the UPDATE event even when combined with
			// other events (which keep PG's fixed INSERT, DELETE, UPDATE order),
			// and a column needing quoting is double-quoted.
			name: "insert or update of quoted column",
			trig: catalog.Trigger{
				Name: "trg_iuof", Timing: catalog.TriggerAfter,
				Events: []string{"insert", "update"}, UpdateColumns: []string{"Mixed"},
				ForEachRow: false, FuncSchema: "public", FuncName: "g",
			},
			want: `CREATE TRIGGER trg_iuof AFTER INSERT OR UPDATE OF "Mixed" ON public.t FOR EACH STATEMENT EXECUTE FUNCTION public.g()`,
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
		{
			// DU-002 slice 327: a CONSTRAINT TRIGGER with the default deferrability
			// emits `CREATE CONSTRAINT TRIGGER` and `NOT DEFERRABLE INITIALLY
			// IMMEDIATE ` between the ON-table name and FOR EACH ROW.
			name: "constraint trigger not deferrable",
			trig: catalog.Trigger{
				Name: "trg_c", Timing: catalog.TriggerAfter,
				Events: []string{"insert"}, ForEachRow: true,
				IsConstraint: true, ConstraintOID: 16600,
				FuncSchema: "public", FuncName: "f",
			},
			want: "CREATE CONSTRAINT TRIGGER trg_c AFTER INSERT ON public.t NOT DEFERRABLE INITIALLY IMMEDIATE FOR EACH ROW EXECUTE FUNCTION public.f()",
		},
		{
			// DEFERRABLE INITIALLY DEFERRED renders without the leading NOT and with
			// DEFERRED in the INITIALLY slot.
			name: "constraint trigger deferrable initially deferred",
			trig: catalog.Trigger{
				Name: "trg_cd", Timing: catalog.TriggerAfter,
				Events: []string{"update"}, ForEachRow: true,
				IsConstraint: true, ConstraintOID: 16601,
				Deferrable: true, InitDeferred: true,
				FuncSchema: "public", FuncName: "g",
			},
			want: "CREATE CONSTRAINT TRIGGER trg_cd AFTER UPDATE ON public.t DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION public.g()",
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
