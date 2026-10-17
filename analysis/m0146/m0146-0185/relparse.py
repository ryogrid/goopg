import re, sys
# Parse PG OPTIMIZER_DEBUG RELOPTINFO pprint output: per rel, the top-level
# entries of :pathlist with node type, startup/total cost and pathkey presence.
text = open(sys.argv[1]).read()
blocks = text.split('{RELOPTINFO')[1:]
want_rows = sys.argv[2] if len(sys.argv) > 2 else None
for b in blocks:
    m = re.search(r':relids \(b([^)]*)\)', b); rel = m.group(1).strip() if m else '?'
    m = re.search(r':rows (\S+)', b); rows = m.group(1) if m else '?'
    if want_rows and rows != want_rows: continue
    i = b.find(':pathlist (')
    if i < 0: continue
    # walk the parenthesised list, recording top-level {NODE entries (depth 1 inside the list)
    j = i + len(':pathlist (')
    depth = 0; entries = []; start = None
    while j < len(b):
        c = b[j]
        if c == '{':
            if depth == 0: start = j
            depth += 1
        elif c == '}':
            depth -= 1
            if depth == 0: entries.append(b[start:j+1])
        elif c == ')' and depth == 0:
            break
        j += 1
    print(f"REL (b {rel}) rows={rows} npaths={len(entries)}")
    for e in entries:
        t = re.match(r'\{(\w+)', e).group(1)
        sc = re.search(r':(?:jpath\.path\.|path\.)?startup_cost (\S+)', e).group(1)
        tc = re.search(r':(?:jpath\.path\.|path\.)?total_cost (\S+)', e).group(1)
        pk = re.search(r':(?:jpath\.path\.|path\.)?pathkeys (\S+)', e)
        jt = re.search(r':jpath\.jointype (\d+)', e)
        print(f"   {t:22s} {sc:>12s}..{tc:<12s} pathkeys={'none' if (pk and pk.group(1)=='<>') else 'yes'} jt={jt.group(1) if jt else '-'}")
