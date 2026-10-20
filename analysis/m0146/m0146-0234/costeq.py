import re,sys
def secs(f):
    s=open(f).read(); parts=re.split(r'^=+ ?(Q\d+)\s*$',s,flags=re.M)
    return {parts[i]:parts[i+1].splitlines() for i in range(1,len(parts),2)}
G=secs(sys.argv[1]); P=secs(sys.argv[2])
cost=re.compile(r'\(cost=([\d.]+)\.\.([\d.]+) rows=(\d+) width=(\d+)')
tot=same=wsame=0; allq=0
for q in G:
    if q not in P: continue
    g=[l for l in G[q] if cost.search(l)]; p=[l for l in P[q] if cost.search(l)]
    if len(g)!=len(p): continue
    a=True
    for x,y in zip(g,p):
        cx=cost.search(x).groups(); cy=cost.search(y).groups()
        tot+=1; s_=cx[:2]==cy[:2]; same+=s_; a&=s_; wsame+= cx[3]==cy[3]
    allq+=a
print(sys.argv[3],'nodes',tot,'cost-equal',same,'width-equal',wsame,'queries-all-costs-equal',allq)
