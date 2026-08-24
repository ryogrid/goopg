package executor

import "testing"

// TestNormalizeInetCidrText locks down normalizeInetCidrText's canonicalisation
// against PG 18.3's network_in/network_out (postgres/src/backend/utils/adt/
// network.c). Regression tripwire for the inet.sql regress-file root cause
// found in M0134-0130: goopg previously stored inet/cidr column values as raw,
// unnormalised text (no classful-default-mask expansion, no canonical
// non-abbreviated output, no CIDR host-bit validation).
func TestNormalizeInetCidrText(t *testing.T) {
	cases := []struct {
		name    string
		in      string
		isCidr  bool
		want    string
		wantErr string // ExecError.Code, empty = no error
	}{
		// cidr: classful-default-mask expansion (cidrDefaultV4Mask), and the
		// canonical dotted-quad form always shows the netmask, even /32.
		{"cidr classA default", "10", true, "10.0.0.0/8", ""},
		{"cidr classA explicit host", "10.0.0.0", true, "10.0.0.0/32", ""},
		{"cidr classB default", "10.1", true, "10.1.0.0/16", ""},
		{"cidr classC default", "192.168.1", true, "192.168.1.0/24", ""},
		{"cidr explicit slash", "192.168.1.0/26", true, "192.168.1.0/26", ""},
		{"cidr full host", "10.1.2.3", true, "10.1.2.3/32", ""},

		// inet: no classful default (always /32 v4 / /128 v6 unless given),
		// and the mask suffix is OMITTED at full width (unlike cidr).
		{"inet no mask", "192.168.1.226", false, "192.168.1.226", ""},
		{"inet with mask", "192.168.1.226/24", false, "192.168.1.226/24", ""},

		// cidr host-bit violation: 22P02 + DETAIL, not silently masked.
		{"cidr host bits set", "192.168.1.2/30", true, "", "22P02"},

		// malformed address: 22P02, no DETAIL distinction needed here.
		{"cidr bad address", "1234::1234::1234", true, "", "22P02"},

		// IPv6: "::"-compression and the PG-specific embedded-v4 tail, which
		// Go's net.IP.String() gets wrong for both of these inputs.
		{"inet v6 mapped v4", "::ffff:1.2.3.4", false, "::ffff:1.2.3.4", ""},
		{"inet v6 compat v4", "::4.3.2.1/24", false, "::4.3.2.1/24", ""},
		{"inet v6 plain", "10:23::f1", false, "10:23::f1", ""},
		{"inet v6 with mask", "10:23::8000/113", false, "10:23::8000/113", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := normalizeInetCidrText(tc.in, tc.isCidr)
			if tc.wantErr != "" {
				if err == nil {
					t.Fatalf("normalizeInetCidrText(%q, %v) = %q, nil; want error %s", tc.in, tc.isCidr, got, tc.wantErr)
				}
				if err.Code != tc.wantErr {
					t.Fatalf("normalizeInetCidrText(%q, %v) error code = %s; want %s", tc.in, tc.isCidr, err.Code, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("normalizeInetCidrText(%q, %v) unexpected error: %v", tc.in, tc.isCidr, err)
			}
			if got != tc.want {
				t.Fatalf("normalizeInetCidrText(%q, %v) = %q; want %q", tc.in, tc.isCidr, got, tc.want)
			}
		})
	}
}
