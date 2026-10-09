-- M0146-0034 repro (goopg TPC-DS SF0.25 clone; 6/12 runs wrong on e379ea18b)
-- expected 2450815 every time (= min(d_date_sk) where d_year = 1998)
select d_date_sk from date_dim where d_year = 1998 order by d_date_sk limit 1;
-- plan: Limit -> Gather (Workers Planned: 1) -> Parallel Index Scan using date_dim_pkey
