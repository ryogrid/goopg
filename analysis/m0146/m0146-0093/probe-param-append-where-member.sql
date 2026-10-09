CREATE TABLE li (id int primary key, cat int);
CREATE TABLE cs1 (item int, ord int, amt int, primary key (item, ord));
CREATE TABLE ws1 (item int, ord int, amt int, primary key (item, ord));
INSERT INTO li SELECT g, g%50 FROM generate_series(1,2000) g;
INSERT INTO cs1 SELECT g%2000+1, g, g FROM generate_series(1,100000) g;
INSERT INTO ws1 SELECT g%2000+1, g, g FROM generate_series(1,50000) g;
ANALYZE li; ANALYZE cs1; ANALYZE ws1;
EXPLAIN (COSTS OFF) SELECT li.id, x.amt FROM li, (SELECT item, amt FROM cs1 WHERE amt > 5 UNION ALL SELECT item, amt FROM ws1) x WHERE x.item = li.id AND li.cat = 3;
