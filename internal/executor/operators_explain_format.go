package executor

// EXPLAIN FORMAT {JSON,XML,YAML} tree serialization (M0122-0003).
//
// planToJSON / planToJSONWithStats (operators_explain.go) already build a
// generic map[string]any tree shaped exactly like upstream's structured
// EXPLAIN output: {"Node Type": ..., "Plans": [...]}. FORMAT JSON just
// json.Marshals that tree. FORMAT XML and FORMAT YAML are alternate
// serializations of the SAME tree, so this file walks it once per format
// instead of building three independent renderers from the plan tree.
//
// Mirrors postgres/src/backend/commands/explain_format.c's
// ExplainProperty*/ExplainOpenGroup/ExplainXMLTag/escape_yaml family:
//   - XML tag names are sanitized (only [A-Za-z0-9-_.], else '-') since XML
//     forbids whitespace/slashes in tag names (ExplainXMLTag).
//   - A "Plans" array's elements render as singular "<Plan>" tags, matching
//     upstream's ExplainOpenGroup("Plans", "Plans", false, es) wrapping
//     per-child ExplainNode's own ExplainOpenGroup("Plan", ...).
//   - YAML string values are JSON-quoted (escape_yaml literally calls
//     escape_json upstream — YAML is a JSON superset, so this sidesteps
//     YAML's notoriously complex quoting rules).
//   - YAML's first property of an unlabeled list item ("- ") stays inline
//     with the dash; a labeled group's properties (and every subsequent
//     item) start on their own indented line (ExplainYAMLLineStarting).
//
// Field order is alphabetical (sortedKeys) rather than upstream's fixed
// per-node-type order, matching this codebase's existing FORMAT JSON
// behaviour (json.Marshal on a map[string]any already sorts keys) — key
// order is not semantically significant and no caller depends on it.

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/goopg/goopg/internal/parser"
)

// renderExplainTree serializes root (the {"Plan": ..., "Planning Time": ...,
// "Execution Time": ...} wrapper built by explainOp.Open) in the requested
// format. JSON keeps the existing json.MarshalIndent behaviour; XML/YAML
// walk the same tree via renderExplainXML/renderExplainYAML.
func renderExplainTree(format parser.ExplainFormat, root map[string]any) (string, error) {
	switch format {
	case parser.ExplainFormatJSON:
		out, err := json.MarshalIndent([]any{root}, "", "  ")
		if err != nil {
			return "", fmt.Errorf("explain: marshal JSON: %w", err)
		}
		return string(out), nil
	case parser.ExplainFormatXML:
		return renderExplainXML(root), nil
	case parser.ExplainFormatYAML:
		return renderExplainYAML(root), nil
	default:
		return "", fmt.Errorf("explain: unsupported format %v", format)
	}
}

func sortedKeys(obj map[string]any) []string {
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// --- XML ---

func renderExplainXML(root map[string]any) string {
	var b strings.Builder
	b.WriteString("<explain xmlns=\"http://www.postgresql.org/2009/explain\">\n")
	writeXMLGroup(&b, "Query", root, 1)
	b.WriteString("</explain>")
	return b.String()
}

// writeXMLGroup renders obj as a labeled XML group: "<tag>\n<props>\n</tag>\n".
func writeXMLGroup(b *strings.Builder, tag string, obj map[string]any, indent int) {
	pad := strings.Repeat("  ", indent)
	name := xmlTagName(tag)
	b.WriteString(pad)
	b.WriteString("<" + name + ">\n")
	for _, k := range sortedKeys(obj) {
		writeXMLKeyedValue(b, k, obj[k], indent+1)
	}
	b.WriteString(pad)
	b.WriteString("</" + name + ">\n")
}

func writeXMLKeyedValue(b *strings.Builder, key string, v any, indent int) {
	pad := strings.Repeat("  ", indent)
	tag := xmlTagName(key)
	switch val := v.(type) {
	case map[string]any:
		writeXMLGroup(b, key, val, indent)
	case []map[string]any:
		b.WriteString(pad + "<" + tag + ">\n")
		childTag := xmlSingularTag(key)
		for _, child := range val {
			writeXMLGroup(b, childTag, child, indent+1)
		}
		b.WriteString(pad + "</" + tag + ">\n")
	case []string:
		// ExplainPropertyList: an unlabeled list of scalar items, each
		// wrapped in "<Item>" (used for VERBOSE's "Output" column list).
		b.WriteString(pad + "<" + tag + ">\n")
		itemPad := strings.Repeat("  ", indent+1)
		for _, item := range val {
			b.WriteString(itemPad + "<Item>" + xmlEscapeText(item) + "</Item>\n")
		}
		b.WriteString(pad + "</" + tag + ">\n")
	default:
		b.WriteString(pad + "<" + tag + ">" + xmlEscapeText(fmt.Sprint(val)) + "</" + tag + ">\n")
	}
}

// xmlSingularTag derives a child element tag from its containing array's
// key, e.g. "Plans" -> "Plan" (mirrors upstream's ExplainOpenGroup("Plans",
// "Plans", false, es) wrapping per-child ExplainOpenGroup("Plan", ...)).
func xmlSingularTag(key string) string {
	if strings.HasSuffix(key, "s") && len(key) > 1 {
		return key[:len(key)-1]
	}
	return key
}

// xmlTagName sanitizes name into a valid XML tag: PostgreSQL replaces any
// character outside [A-Za-z0-9-_.] with '-' (ExplainXMLTag) since XML tag
// names can't contain whitespace or slashes, e.g. "Node Type" -> "Node-Type".
func xmlTagName(name string) string {
	var b strings.Builder
	for _, r := range name {
		switch {
		case r >= 'A' && r <= 'Z', r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return b.String()
}

// xmlEscapeText escapes the characters PostgreSQL's escape_xml treats as
// unsafe to embed directly in XML text content.
func xmlEscapeText(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch r {
		case '&':
			b.WriteString("&amp;")
		case '<':
			b.WriteString("&lt;")
		case '>':
			b.WriteString("&gt;")
		case '\r':
			b.WriteString("&#x0d;")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// --- YAML ---

func renderExplainYAML(root map[string]any) string {
	var b strings.Builder
	// The top-level Query group is unlabeled (upstream: ExplainOpenGroup
	// ("Query", NULL, true, es)) — introduced by "- ", first property inline.
	b.WriteString("- ")
	writeYAMLObjectBody(&b, root, 1, true)
	return b.String()
}

// writeYAMLObjectBody writes each key: value pair of obj as children of an
// already-opened group at indentLevel (units of 2 spaces). firstInline=true
// means the first property continues on the current line (right after an
// unlabeled "- " list marker); every other property — and all properties of
// a labeled group — starts with "\n"+indent (ExplainYAMLLineStarting).
func writeYAMLObjectBody(b *strings.Builder, obj map[string]any, indentLevel int, firstInline bool) {
	for i, k := range sortedKeys(obj) {
		if i != 0 || !firstInline {
			b.WriteByte('\n')
			b.WriteString(strings.Repeat("  ", indentLevel))
		}
		writeYAMLKeyedValue(b, k, obj[k], indentLevel)
	}
}

func writeYAMLKeyedValue(b *strings.Builder, key string, v any, indentLevel int) {
	switch val := v.(type) {
	case map[string]any:
		b.WriteString(key + ":")
		// Labeled nested group: children always start on their own line.
		writeYAMLObjectBody(b, val, indentLevel+1, false)
	case []map[string]any:
		b.WriteString(key + ":")
		for _, child := range val {
			b.WriteByte('\n')
			b.WriteString(strings.Repeat("  ", indentLevel+1) + "- ")
			writeYAMLObjectBody(b, child, indentLevel+2, true)
		}
	case []string:
		b.WriteString(key + ":")
		itemIndent := strings.Repeat("  ", indentLevel+1)
		for _, item := range val {
			b.WriteByte('\n')
			b.WriteString(itemIndent + "- " + yamlScalar(item))
		}
	default:
		b.WriteString(key + ": " + yamlScalar(val))
	}
}

// yamlScalar renders a leaf value the way upstream's escape_yaml (== JSON
// quoting for strings) and numeric-mode ExplainProperty (bare token) do.
func yamlScalar(v any) string {
	switch val := v.(type) {
	case string:
		out, _ := json.Marshal(val)
		return string(out)
	case bool:
		if val {
			return "true"
		}
		return "false"
	case int:
		return strconv.Itoa(val)
	case int64:
		return strconv.FormatInt(val, 10)
	case float64:
		return strconv.FormatFloat(val, 'f', -1, 64)
	default:
		return fmt.Sprint(val)
	}
}
