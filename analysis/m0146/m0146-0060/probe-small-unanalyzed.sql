drop table if exists items_na;
create table items_na(id int, label text); insert into items_na values (1,'a'),(2,'beta'),(3,'c');
create index items_na_id on items_na(id);
explain select id, label from items_na where id = 2;
explain select id, label from items_na where id >= 2;
select relpages, reltuples from pg_class where relname='items_na';
