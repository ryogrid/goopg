package auth

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ParseHBAFile reads a pg_hba.conf-style file and returns its rules.
// include / include_if_exists / include_dir directives are honoured
// recursively. Cycles abort parsing.
func ParseHBAFile(path string) (*RuleSet, error) {
	visited := map[string]bool{}
	rs := &RuleSet{}
	if err := parseHBAFileInto(path, false, rs, visited); err != nil {
		return nil, err
	}
	return rs, nil
}

// ParseHBAReader is the io.Reader form of ParseHBAFile, intended for
// tests that don't want to touch the filesystem. The label appears in
// SourceFile diagnostics; pass "" if you don't care.
func ParseHBAReader(r io.Reader, label string) (*RuleSet, error) {
	rs := &RuleSet{}
	visited := map[string]bool{}
	if err := parseHBAStream(r, label, rs, visited); err != nil {
		return nil, err
	}
	return rs, nil
}

func parseHBAFileInto(path string, optional bool, rs *RuleSet, visited map[string]bool) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", path, err)
	}
	if visited[abs] {
		return fmt.Errorf("include cycle detected at %s", abs)
	}
	visited[abs] = true
	defer delete(visited, abs)

	f, err := os.Open(path)
	if err != nil {
		if optional && errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()
	return parseHBAStream(f, abs, rs, visited)
}

func parseHBAStream(r io.Reader, label string, rs *RuleSet, visited map[string]bool) error {
	scanner := bufio.NewScanner(r)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		raw := scanner.Text()
		if !looksLikeRule(raw) {
			continue
		}
		tokens, err := tokenize(raw)
		if err != nil {
			return ParseError{File: label, Line: lineNo, Err: err}
		}
		if len(tokens) == 0 {
			continue
		}
		if directive, arg, ok := includeDirective(tokens); ok {
			resolved := resolveIncludePath(label, arg)
			switch directive {
			case "include":
				if err := parseHBAFileInto(resolved, false, rs, visited); err != nil {
					return ParseError{File: label, Line: lineNo, Err: err}
				}
			case "include_if_exists":
				if err := parseHBAFileInto(resolved, true, rs, visited); err != nil {
					return ParseError{File: label, Line: lineNo, Err: err}
				}
			case "include_dir":
				if err := parseHBAIncludeDir(resolved, rs, visited); err != nil {
					return ParseError{File: label, Line: lineNo, Err: err}
				}
			}
			continue
		}
		rule, err := parseRule(tokens)
		if err != nil {
			return ParseError{File: label, Line: lineNo, Err: err}
		}
		rule.SourceFile = label
		rule.SourceLine = lineNo
		rs.Rules = append(rs.Rules, rule)
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read %s: %w", label, err)
	}
	return nil
}

func parseHBAIncludeDir(dir string, rs *RuleSet, visited map[string]bool) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("include_dir %s: %w", dir, err)
	}
	var conf []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".conf") {
			continue
		}
		conf = append(conf, filepath.Join(dir, e.Name()))
	}
	sort.Strings(conf)
	for _, p := range conf {
		if err := parseHBAFileInto(p, false, rs, visited); err != nil {
			return err
		}
	}
	return nil
}

func resolveIncludePath(parent, arg string) string {
	if filepath.IsAbs(arg) {
		return arg
	}
	if parent == "" {
		return arg
	}
	return filepath.Join(filepath.Dir(parent), arg)
}

func looksLikeRule(line string) bool {
	for _, b := range line {
		if b == ' ' || b == '\t' {
			continue
		}
		return b != '#'
	}
	return false
}

// tokenize splits a line into whitespace-separated tokens, honouring
// double quotes (which preserve internal whitespace and disable special
// keyword handling) and treating "#" as a comment when it starts a token.
//
// We do not support shell-style escapes. pg_hba.conf doesn't either —
// hba.c's tokenize only strips surrounding quotes.
func tokenize(line string) ([]string, error) {
	var tokens []string
	var cur strings.Builder
	inQuote := false
	flush := func() {
		if cur.Len() > 0 {
			tokens = append(tokens, cur.String())
			cur.Reset()
		}
	}
	for i := 0; i < len(line); i++ {
		c := line[i]
		if inQuote {
			if c == '"' {
				inQuote = false
				continue
			}
			cur.WriteByte(c)
			continue
		}
		switch {
		case c == '"':
			inQuote = true
		case c == '#' && cur.Len() == 0:
			return tokens, nil
		case c == ' ' || c == '\t':
			flush()
		default:
			cur.WriteByte(c)
		}
	}
	if inQuote {
		return nil, errors.New("unterminated quote")
	}
	flush()
	return tokens, nil
}

func includeDirective(tokens []string) (kind, arg string, ok bool) {
	if len(tokens) < 2 {
		return "", "", false
	}
	switch tokens[0] {
	case "include", "include_if_exists", "include_dir":
		return tokens[0], tokens[1], true
	}
	return "", "", false
}

// parseRule turns tokens into a Rule. Format:
//
//	local    DATABASE USER         METHOD [OPTIONS]
//	host*    DATABASE USER ADDRESS METHOD [OPTIONS]
//	host*    DATABASE USER IP MASK METHOD [OPTIONS]
func parseRule(tokens []string) (Rule, error) {
	if len(tokens) < 4 {
		return Rule{}, fmt.Errorf("expected at least 4 fields, got %d", len(tokens))
	}
	connType, ok := connTypeNames[tokens[0]]
	if !ok {
		return Rule{}, fmt.Errorf("unknown connection type %q", tokens[0])
	}
	rule := Rule{
		ConnType: connType,
		Database: splitNameList(tokens[1]),
		User:     splitNameList(tokens[2]),
		Options:  map[string]string{},
	}

	rest := tokens[3:]
	if connType == ConnLocal {
		// local DATABASE USER METHOD [OPTIONS]
		method, opts, err := parseMethodAndOptions(rest)
		if err != nil {
			return Rule{}, err
		}
		rule.Method = method
		for k, v := range opts {
			rule.Options[k] = v
		}
		return rule, nil
	}

	if len(rest) < 2 {
		return Rule{}, fmt.Errorf("expected ADDRESS METHOD [OPTIONS], got %d tokens", len(rest))
	}
	address := rest[0]
	rest = rest[1:]

	switch address {
	case "all":
		rule.AllAddresses = true
	case "samehost":
		rule.SameHost = true
	case "samenet":
		rule.SameNet = true
	default:
		net, consumedSecondToken, err := parseAddressField(address, rest)
		if err != nil {
			return Rule{}, err
		}
		rule.Network = net
		if consumedSecondToken {
			if len(rest) < 1 {
				return Rule{}, errors.New("expected METHOD after IP+MASK pair")
			}
			rest = rest[1:]
		}
	}

	method, opts, err := parseMethodAndOptions(rest)
	if err != nil {
		return Rule{}, err
	}
	rule.Method = method
	for k, v := range opts {
		rule.Options[k] = v
	}
	return rule, nil
}

func splitNameList(s string) []string {
	parts := strings.Split(s, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// parseAddressField interprets the ADDRESS field. It returns the parsed
// network and whether the *next* token was consumed (the legacy two-token
// IP+netmask form).
func parseAddressField(token string, rest []string) (*net.IPNet, bool, error) {
	if strings.Contains(token, "/") {
		_, network, err := net.ParseCIDR(token)
		if err != nil {
			return nil, false, fmt.Errorf("invalid CIDR address %q: %w", token, err)
		}
		return network, false, nil
	}
	// Try the legacy two-token form: IP MASK.
	if len(rest) == 0 {
		return nil, false, fmt.Errorf("address %q is missing /CIDR or netmask", token)
	}
	ip := net.ParseIP(token)
	if ip == nil {
		// Hostnames and dot-prefixed suffixes — recognised but not
		// implemented. Construct a never-matching net so the rule
		// effectively rejects, and don't error out: the operator will
		// see the implicit reject in logs and can pick CIDR instead.
		return &net.IPNet{IP: net.IP{0, 0, 0, 0}, Mask: net.IPMask{255, 255, 255, 255}}, false,
			fmt.Errorf("hostname-style ADDRESS %q: %w", token, ErrUnsupportedSyntax)
	}
	mask := net.ParseIP(rest[0])
	if mask == nil {
		return nil, false, fmt.Errorf("invalid netmask %q", rest[0])
	}
	if v4 := ip.To4(); v4 != nil {
		mv4 := mask.To4()
		if mv4 == nil {
			return nil, false, fmt.Errorf("address/mask family mismatch (%q / %q)", token, rest[0])
		}
		return &net.IPNet{IP: v4, Mask: net.IPMask(mv4)}, true, nil
	}
	if mv6 := mask.To16(); mv6 != nil {
		return &net.IPNet{IP: ip.To16(), Mask: net.IPMask(mv6)}, true, nil
	}
	return nil, false, fmt.Errorf("invalid address/mask combination (%q / %q)", token, rest[0])
}

func parseMethodAndOptions(tokens []string) (Method, map[string]string, error) {
	if len(tokens) == 0 {
		return 0, nil, errors.New("missing METHOD")
	}
	method, ok := methodNames[tokens[0]]
	if !ok {
		return 0, nil, fmt.Errorf("unknown authentication method %q", tokens[0])
	}
	opts := map[string]string{}
	for _, opt := range tokens[1:] {
		k, v, ok := strings.Cut(opt, "=")
		if !ok {
			return 0, nil, fmt.Errorf("option %q is not key=value", opt)
		}
		opts[k] = v
	}
	return method, opts, nil
}
