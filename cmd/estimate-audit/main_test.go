package main

import (
	"os"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

// TestSelectQueriesParsesValidSpecs pins the accepted forms.
func TestSelectQueriesParsesValidSpecs(t *testing.T) {
	cases := []struct {
		spec string
		want []int
	}{
		{"1", []int{1}},
		{"3,1", []int{1, 3}},
		{"5-7", []int{5, 6, 7}},
		{" 2 , 4-5 ", []int{2, 4, 5}},
	}
	for _, tc := range cases {
		if got := selectQueries(tc.spec); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("selectQueries(%q) = %v, want %v", tc.spec, got, tc.want)
		}
	}
}

// TestSelectQueriesRejectsMalformedSpecs covers review/260831-2 CM-5: the Atoi
// errors used to be dropped, so `--queries 5-` produced hi=0, the 5..0 loop
// selected nothing, and the tool audited ZERO queries while reporting success;
// `--queries -5` asked for a nonexistent "Q-5". Both must now exit non-zero
// with a message. selectQueries calls fatal (os.Exit), so each case runs in a
// re-exec of this test binary.
func TestSelectQueriesRejectsMalformedSpecs(t *testing.T) {
	for _, spec := range []string{"5-", "-5", "3-x", "abc", "0", "9-4"} {
		if os.Getenv("ESTIMATE_AUDIT_BAD_SPEC") == spec {
			selectQueries(spec)
			return
		}
	}
	for _, spec := range []string{"5-", "-5", "3-x", "abc", "0", "9-4"} {
		cmd := exec.Command(os.Args[0], "-test.run=TestSelectQueriesRejectsMalformedSpecs")
		cmd.Env = append(os.Environ(), "ESTIMATE_AUDIT_BAD_SPEC="+spec)
		out, err := cmd.CombinedOutput()
		if err == nil {
			t.Errorf("selectQueries(%q) accepted the spec; want a fatal error", spec)
			continue
		}
		if !strings.Contains(string(out), "--queries") {
			t.Errorf("selectQueries(%q): message %q lacks the option name", spec, out)
		}
	}
}
