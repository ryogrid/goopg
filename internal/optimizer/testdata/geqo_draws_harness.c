/*
 * Draw-order oracle for goopg's GEQO (M0146-0214). It compiles PG 18.3's own
 * GEQO sources (geqo_random.c, geqo_recombination.c, geqo_selection.c,
 * geqo_copy.c, geqo_erx.c) against the real pg_prng from libpgcommon.a. It
 * then prints, for each seed and problem size, the tours random_init_pool
 * draws, a sequence of geqo_selection parent pairs, and ERX children
 * (gimme_edge_table + gimme_tour). palloc and elog are the only stubs. The
 * golden file is testdata/geqo_draws_golden.txt; regenerate it with:
 *
 *   gcc -I postgres/src/include -o /tmp/geqo_draws_harness \
 *       internal/optimizer/testdata/geqo_draws_harness.c \
 *       postgres/local_install/lib/libpgcommon.a \
 *       postgres/local_install/lib/libpgport.a -lm
 *   /tmp/geqo_draws_harness > internal/optimizer/testdata/geqo_draws_golden.txt
 */
#include "postgres.h"

#include <stdio.h>
#include <stdlib.h>

#include "optimizer/geqo.h"
#include "optimizer/geqo_copy.h"
#include "optimizer/geqo_pool.h"
#include "optimizer/geqo_random.h"
#include "optimizer/geqo_recombination.h"
#include "optimizer/geqo_selection.h"

/* --- backend stubs ------------------------------------------------------ */
void *
palloc(Size size)
{
	return malloc(size);
}

void *
palloc0(Size size)
{
	return calloc(1, size);
}

void
pfree(void *p)
{
	free(p);
}

bool
errstart(int elevel, const char *domain)
{
	return elevel >= ERROR;
}

bool
errstart_cold(int elevel, const char *domain)
{
	return errstart(elevel, domain);
}

void
errfinish(const char *filename, int lineno, const char *funcname)
{
	fprintf(stderr, "elog at %s:%d (%s)\n", filename, lineno, funcname);
	abort();
}

int
errmsg_internal(const char *fmt,...)
{
	return 0;
}

double		Geqo_seed = 0;

/* --- the PG sources under test ----------------------------------------- */
#include "../../../postgres/src/backend/optimizer/geqo/geqo_random.c"
#include "../../../postgres/src/backend/optimizer/geqo/geqo_recombination.c"
#include "../../../postgres/src/backend/optimizer/geqo/geqo_copy.c"
#include "../../../postgres/src/backend/optimizer/geqo/geqo_selection.c"
#include "../../../postgres/src/backend/optimizer/geqo/geqo_erx.c"

static void
print_tour(const char *tag, Gene *t, int n)
{
	printf("%s", tag);
	for (int i = 0; i < n; i++)
		printf("%s%d", i ? "," : "", (int) t[i]);
	printf("\n");
}

static void
run(double seed, int num_gene, int pool_size)
{
	PlannerInfo root;
	GeqoPrivateData private;
	Pool		pool;
	Chromosome	momma,
				daddy;
	Edge	   *edge_table;

	memset(&root, 0, sizeof(root));
	memset(&private, 0, sizeof(private));
	root.join_search_private = &private;
	geqo_set_seed(&root, seed);

	pool.size = pool_size;
	pool.string_length = num_gene;
	pool.data = palloc(pool_size * sizeof(Chromosome));
	for (int i = 0; i < pool_size; i++)
	{
		pool.data[i].string = palloc((num_gene + 1) * sizeof(Gene));
		pool.data[i].worth = 0;
	}
	momma.string = palloc((num_gene + 1) * sizeof(Gene));
	daddy.string = palloc((num_gene + 1) * sizeof(Gene));

	printf("case seed=%g genes=%d pool=%d\n", seed, num_gene, pool_size);
	for (int i = 0; i < pool_size; i++)
	{
		init_tour(&root, pool.data[i].string, num_gene);
		/* tag each individual by its pool position for selection output */
		pool.data[i].worth = i;
		print_tour("init ", pool.data[i].string, num_gene);
	}
	edge_table = alloc_edge_table(&root, num_gene);
	for (int g = 0; g < 12; g++)
	{
		geqo_selection(&root, &momma, &daddy, &pool, 2.0);
		printf("sel %d,%d\n", (int) momma.worth, (int) daddy.worth);
		gimme_edge_table(&root, momma.string, daddy.string, num_gene, edge_table);
		gimme_tour(&root, edge_table, momma.string, num_gene);
		print_tour("kid ", momma.string, num_gene);
	}
}

int
main(int argc, char **argv)
{
	run(0.0, 4, 40);
	run(0.0, 12, 250);
	run(0.0, 18, 250);
	run(0.5, 13, 250);
	return 0;
}
