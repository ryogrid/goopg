package executor

import "testing"

// TestXMLWellFormedness is the M0134-0188 guard.
//
// Before this fix goopg had NO evalCast arm for "xml", so
// `'<wrong'::xml` and an implicit column-coercion INSERT of the same
// malformed fragment both succeeded and stored the value verbatim. Both the
// explicit-cast path (evalCast) and the implicit column-coercion path
// (encodeValuePGCtx, codec.go) must now reject a not-well-formed value with
// the same SQLSTATE PG's xml_parse raises (2200N content / 2200M document).
//
// Expectations captured from a live PostgreSQL 18.3 oracle
// (postgres/local_install, `psql -f`).
func TestXMLWellFormedness(t *testing.T) {
	t.Run("evalCast content mode (default)", func(t *testing.T) {
		cases := []struct {
			name    string
			in      string
			wantErr bool
		}{
			{"well-formed element", "<value>one</value>", false},
			{"self-closing", "<foo/>", false},
			{"unterminated start tag", "<wrong", true},
			{"unterminated end tag", "<value>one</", true},
			{"mismatched tags", "<a></b>", true},
			{"plain text fragment (valid CONTENT)", "hello", false},
		}
		for _, c := range cases {
			t.Run(c.name, func(t *testing.T) {
				_, err := evalCast(NewStringDatum(c.in), "xml", 0, nil)
				if c.wantErr && err == nil {
					t.Fatalf("evalCast(%q, xml): want error, got none", c.in)
				}
				if !c.wantErr && err != nil {
					t.Fatalf("evalCast(%q, xml): want no error, got %v", c.in, err)
				}
				if err != nil {
					ee, ok := err.(*ExecError)
					if !ok {
						t.Fatalf("evalCast(%q, xml): error is not *ExecError: %v", c.in, err)
					}
					if ee.Code != "2200N" {
						t.Fatalf("evalCast(%q, xml): Code = %q, want 2200N", c.in, ee.Code)
					}
				}
			})
		}
	})

	t.Run("multiple top-level elements is valid CONTENT but invalid DOCUMENT", func(t *testing.T) {
		if err := xmlValidate("<a/><b/>", "content"); err != nil {
			t.Fatalf("content mode: unexpected error: %v", err)
		}
		ee := xmlValidate("<a/><b/>", "document")
		if ee == nil {
			t.Fatalf("document mode: want error for multiple roots, got none")
		}
		if ee.Code != "2200M" {
			t.Fatalf("document mode: Code = %q, want 2200M", ee.Code)
		}
	})

	t.Run("well-formed round-trips through evalCast unchanged", func(t *testing.T) {
		got, err := evalCast(NewStringDatum("<a>1</a>"), "xml", 0, nil)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got.StringValue() != "<a>1</a>" {
			t.Fatalf("got %q, want %q", got.StringValue(), "<a>1</a>")
		}
	})
}
