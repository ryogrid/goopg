import re,sys,collections
sites={}   # node -> (algo,type,buildLeft,width,bound)
ret={}     # node -> (rows,width)
sortsites={}
sortret={}
FULL=9223372036854775807
for path in sys.argv[1:]:
    for ln in open(path,errors='replace'):
        if 'E13CENSUS' not in ln: continue
        if ' join algo=' in ln:
            m=re.search(r'algo=(\d+) type=(\d+) buildLeft=(\w+) buildWidth=(\d+) buildBound=(-?\d+) node=(0x\w+)',ln)
            if m: sites[m.group(6)]=(int(m.group(1)),int(m.group(2)),m.group(3)=='true',int(m.group(4)),int(m.group(5)))
        elif ' join-retained ' in ln:
            m=re.search(r'node=(0x\w+) rows=(\d+) width=(\d+)',ln)
            if m:
                k=m.group(1); r=int(m.group(2)); w=int(m.group(3))
                pr,_=ret.get(k,(0,w)); ret[k]=(pr+r,w)
        elif ' sort childWidth=' in ln:
            m=re.search(r'childWidth=(\d+) childBound=(-?\d+) node=(0x\w+)',ln)
            if m: sortsites[m.group(3)]=(int(m.group(1)),int(m.group(2)))
        elif ' sort-retained ' in ln:
            m=re.search(r'node=(0x\w+) rows=(\d+) width=(\d+)',ln)
            if m:
                k=m.group(1); r=int(m.group(2)); w=int(m.group(3))
                pr,_=sortret.get(k,(0,w)); sortret[k]=(pr+r,w)

print("=== HASH BUILD SITES that actually retained rows ===")
tot_dead=0; tot_cells=0; tot_rows=0
rows_out=[]
for n,(rows,w) in ret.items():
    st=sites.get(n)
    if st is None:
        rows_out.append((rows,w,None,None,None)); continue
    algo,typ,bl,sw,b=st
    dead = 0 if b==FULL or b<0 or b>=w else (w-b)
    tot_dead += rows*dead; tot_cells += rows*w; tot_rows += rows
    rows_out.append((rows,w,typ,b,dead))
for r in sorted(rows_out,reverse=True):
    print(r)
print(f"TOTAL retained rows={tot_rows} cells={tot_cells} ({tot_cells*48/1e6:.1f} MB of Datum cells)")
print(f"PREFIX-DEAD cells={tot_dead} ({tot_dead*48/1e6:.1f} MB) = {100.0*tot_dead/max(tot_cells,1):.2f}% of retained cells")
print()
print("=== SEMI/ANTI hash build sites (Cut A candidates) ===")
sa_rows=0; sa_cells=0
for n,(algo,typ,bl,w,b) in sites.items():
    if typ in (5,6) and algo==1:
        rows,rw = ret.get(n,(0,w))
        sa_rows += rows; sa_cells += rows*w
        print(f"  type={'SEMI' if typ==5 else 'ANTI'} buildWidth={w} bound={'FULL' if b==FULL else b} retainedRows={rows}")
print(f"  SEMI/ANTI retained rows={sa_rows} cells={sa_cells} ({sa_cells*48/1e6:.1f} MB); Cut A (zero-width) would free the cells + payload, keeping 24 B/row slice headers")
print()
print("=== JOIN TYPE census (all built hash joins) ===")
c=collections.Counter((a,t) for (a,t,bl,w,b) in sites.values())
for k,v in sorted(c.items()): print("algo=%d type=%d count=%d"%(k[0],k[1],v))
print()
print("=== SORT SITES ===")
tot_sd=0; tot_sc=0
for n,(rows,w) in sortret.items():
    st=sortsites.get(n)
    if st is None: print(("sort",rows,w,"no-site")); continue
    cw,b=st
    dead = 0 if b==FULL or b<0 or b>=cw else (cw-b)
    tot_sd += rows*dead; tot_sc += rows*cw
    print(("sort",rows,cw,b,dead))
print(f"SORT total cells={tot_sc} ({tot_sc*48/1e6:.1f} MB), prefix-dead={tot_sd} ({tot_sd*48/1e6:.1f} MB)")
print()
print("=== SORT SITES seen at Build (bound vs width), no row counts in this run ===")
for n,(cw,b) in sorted(sortsites.items(), key=lambda kv:-kv[1][0]):
    print(f"childWidth={cw} childBound={'FULL' if b==FULL else b} dead={0 if b==FULL else max(0,cw-b)}")
