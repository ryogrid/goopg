import re, sys
# Print the path-node tree (type + costs) of each pathlist entry of rels whose
# relids/rows match: argv = log, relids ("1 2 3"), rows.
text = open(sys.argv[1]).read()
want_rel, want_rows = sys.argv[2], sys.argv[3]
seen = set()
for b in text.split('{RELOPTINFO')[1:]:
    m = re.search(r':relids \(b([^)]*)\)', b)
    if not m or m.group(1).strip() != want_rel: continue
    if re.search(r':rows (\S+)', b).group(1) != want_rows: continue
    i = b.find(':pathlist (')
    seg = b[i:b.find(':partial_pathlist', i)]
    out = []
    depth = 0
    for tok in re.finditer(r'\{(\w+PATH)|\{|\}|:(?:jpath\.path\.|path\.)?(startup_cost|total_cost) (\S+)', seg):
        if tok.group(1):
            depth += 1; out.append(['  '*depth + tok.group(1), None, None, depth])
        elif tok.group(0) == '{':
            depth += 1; out.append(None)
        elif tok.group(0) == '}':
            depth -= 1
        elif tok.group(2):
            # attach to most recent path node entry
            for e in reversed(out):
                if e is not None:
                    if tok.group(2) == 'startup_cost' and e[1] is None: e[1] = tok.group(3)
                    elif tok.group(2) == 'total_cost' and e[2] is None: e[2] = tok.group(3)
                    break
    s = '\n'.join(f"{e[0]} {e[1]}..{e[2]}" for e in out if e)
    if s in seen: continue
    seen.add(s); print(f"== REL (b {want_rel}) rows={want_rows}"); print(s)
