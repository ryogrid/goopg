CREATE TABLE zn (n numeric, t text);
INSERT INTO zn VALUES ('NaN', 'nan'), ('Infinity', 'inf'), ('-Infinity', 'ninf'), (2, 'two'), (1.0000000000000000000000, 'onebig'), (1, 'one');
SELECT count(*) FROM zn;
SELECT t, n FROM zn ORDER BY t;
CREATE TABLE zm (k numeric, v text);
INSERT INTO zm VALUES ('NaN', 'm-nan'), (1, 'm-one'), (2, 'm-two'), ('Infinity', 'm-inf');
SELECT zn.t, zm.v FROM zn JOIN zm ON zn.n = zm.k ORDER BY 1, 2;
