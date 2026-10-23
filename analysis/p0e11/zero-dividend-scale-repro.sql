-- P0-E11: numeric division of a zero dividend — the result's display scale.
select 0::numeric / 3, 0.00::numeric / 7.5, 0::numeric / 12345.67, (-0.0)::numeric / 2;
