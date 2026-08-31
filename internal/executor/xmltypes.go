package executor

// xmltypes.go — `xml` type input well-formedness validation.
//
// M0134-0188. Until this file existed goopg treated a `xml`-typed value as
// OPAQUE, UNVALIDATED TEXT: `evalCast` had no arm for "xml", so
// `'<wrong'::xml` and `INSERT INTO t(x xml) VALUES ('<value>one</')` both
// SUCCEEDED and stored the malformed fragment verbatim, where PG's xml_in
// (postgres/src/backend/utils/adt/xml.c:273) parses every value through
// libxml2 and rejects anything that is not well-formed. That is the fifth
// instance of the recurring "missing evalCast arm = unvalidated text"
// pattern (xid, circle, float8, and range types were the first four —
// rangetypes.go). pg_input_is_valid/pg_input_error_info (expr.go,
// operators_pg_input_error_info.go) are SIBLING paths to the cast and must
// agree (pattern_sibling_paths_must_agree) — both gained matching "xml" arms
// in the same change.
//
// Scope: this is a well-formedness CHECK, not an XML engine. It answers the
// same yes/no question xml_parse's DOCUMENT/CONTENT gate answers (one root
// element required for DOCUMENT, none for CONTENT) using the Go standard
// library's XML tokenizer instead of libxml2, so it does not reproduce
// libxml2's DETAIL diagnostics (line/column, "Couldn't find end of Start
// Tag …") — only the ERRCODE/top-level message PG raises
// (ERRCODE_INVALID_XML_DOCUMENT "invalid XML document" / 2200M,
// ERRCODE_INVALID_XML_CONTENT "invalid XML content" / 2200N,
// xml.c:1873-1888 xml_parse). The SQL/XML publishing functions
// (XMLELEMENT/XMLFOREST/XMLTABLE/…) and XPath evaluation (xpath/xpath_exists)
// remain unimplemented — REFACTOR-tier grammar + engine work, ledgered
// separately (M0134-0188a/0188b).
//
// The session xmloption GUC (default "content", guc_tables.c:5307) selects
// which grammar production applies; xml_in and every other implicit-parsing
// site (`::xml` cast, column INSERT/UPDATE coercion) reads it via
// ctx.GetSetting, mirroring timeZoneFromCtx's dispatch.

import (
	"encoding/xml"
	"io"
	"strings"
)

// xmlOptionFromCtx resolves the session's xmloption GUC via ctx.GetSetting,
// defaulting to "content" (the PG boot value) when ctx is nil, has no
// GetSetting wired, or the GUC is unset.
func xmlOptionFromCtx(ctx *Context) string {
	if ctx != nil && ctx.GetSetting != nil {
		if v, ok := ctx.GetSetting("xmloption"); ok && v != "" {
			return v
		}
	}
	return "content"
}

// xmlValidate checks s for well-formedness under xmlOption and, if it is not
// well-formed, returns the ExecError xml_in/xml_parse would raise (message
// only — no DETAIL, see file comment). Returns nil when s is well-formed.
func xmlValidate(s, xmlOption string) *ExecError {
	document := strings.EqualFold(xmlOption, "document")
	dec := xml.NewDecoder(strings.NewReader(s))
	dec.Strict = true
	depth := 0
	roots := 0
	sawRootLevelText := false
	for {
		tok, err := dec.Token()
		if err != nil {
			if err != io.EOF {
				return xmlInvalidError(document)
			}
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if depth == 0 {
				roots++
			}
			depth++
		case xml.EndElement:
			depth--
		case xml.CharData:
			if depth == 0 && len(strings.TrimSpace(string(t))) > 0 {
				sawRootLevelText = true
			}
		}
	}
	if document && (roots != 1 || sawRootLevelText) {
		return xmlInvalidError(true)
	}
	return nil
}

func xmlInvalidError(document bool) *ExecError {
	if document {
		return &ExecError{Code: "2200M", Message: "invalid XML document"}
	}
	return &ExecError{Code: "2200N", Message: "invalid XML content"}
}
