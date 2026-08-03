CREATE TABLE dim(k int, y int);
CREATE TABLE fact(a int, b int);
INSERT INTO dim VALUES (1, 10), (2, NULL), (3, 30);
INSERT INTO fact VALUES (1,2),(1,3),(3,2);
SELECT count(d1.y) AS c1, count(d2.y) AS c2, sum(d1.y) AS s1, sum(d2.y) AS s2
FROM fact, dim d1, dim d2 WHERE fact.a = d1.k AND fact.b = d2.k;
SELECT d1.y AS y1, d2.y AS y2, count(d1.y) AS c1, count(d2.y) AS c2
FROM fact, dim d1, dim d2 WHERE fact.a = d1.k AND fact.b = d2.k
GROUP BY d1.y, d2.y ORDER BY 1, 2;
SELECT count(d1.y) AS c1, count(d2.y) AS c2
FROM fact, dim d1, dim d2 WHERE fact.a = d1.k AND fact.b = d2.k
HAVING count(d1.y) > count(d2.y);
