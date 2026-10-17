// Package pgqsort is PostgreSQL's qsort, postgres/src/port/qsort.c: the
// pg_qsort instantiation of postgres/src/include/lib/sort_template.h, a
// Bentley-McIlroy quicksort with med3/ninther pivots, an insertion sort below
// seven elements, and a presorted early exit.
//
// It is not stable. When a PG algorithm sorts data with equal keys, the order
// it leaves those elements in is part of the algorithm's result. GEQO's
// sort_pool is one: it sorts tours by fitness, and the selection that follows
// draws pool positions. Reproducing PG's plan therefore needs PG's
// permutation, not merely a correct sort (M0146-0214).
package pgqsort

// Sort sorts data in place, permuting it exactly as pg_qsort would. cmp
// returns <0, 0 or >0, as the C comparator does.
func Sort[T any](data []T, cmp func(a, b *T) int) {
	a := data
	for {
		n := len(a)
		if n < 7 {
			for pm := 1; pm < n; pm++ {
				for pl := pm; pl > 0 && cmp(&a[pl-1], &a[pl]) > 0; pl-- {
					a[pl], a[pl-1] = a[pl-1], a[pl]
				}
			}
			return
		}
		presorted := true
		for pm := 1; pm < n; pm++ {
			if cmp(&a[pm-1], &a[pm]) > 0 {
				presorted = false
				break
			}
		}
		if presorted {
			return
		}
		pm := n / 2
		if n > 7 {
			pl, pn := 0, n-1
			if n > 40 {
				d := n / 8
				pl = med3(a, pl, pl+d, pl+2*d, cmp)
				pm = med3(a, pm-d, pm, pm+d, cmp)
				pn = med3(a, pn-2*d, pn-d, pn, cmp)
			}
			pm = med3(a, pl, pm, pn, cmp)
		}
		a[0], a[pm] = a[pm], a[0]
		pa, pb := 1, 1
		pc, pd := n-1, n-1
		for {
			for pb <= pc {
				r := cmp(&a[pb], &a[0])
				if r > 0 {
					break
				}
				if r == 0 {
					a[pa], a[pb] = a[pb], a[pa]
					pa++
				}
				pb++
			}
			for pb <= pc {
				r := cmp(&a[pc], &a[0])
				if r < 0 {
					break
				}
				if r == 0 {
					a[pc], a[pd] = a[pd], a[pc]
					pd--
				}
				pc--
			}
			if pb > pc {
				break
			}
			a[pb], a[pc] = a[pc], a[pb]
			pb++
			pc--
		}
		pn := n
		d1 := min(pa, pb-pa)
		swapN(a, 0, pb-d1, d1)
		d1 = min(pd-pc, pn-pd-1)
		swapN(a, pb, pn-d1, d1)
		d1 = pb - pa
		d2 := pd - pc
		if d1 <= d2 {
			// Recurse on the left partition, iterate on the right.
			if d1 > 1 {
				Sort(a[:d1], cmp)
			}
			if d2 <= 1 {
				return
			}
			a = a[pn-d2 : pn]
		} else {
			// Recurse on the right partition, iterate on the left.
			if d2 > 1 {
				Sort(a[pn-d2:pn], cmp)
			}
			if d1 <= 1 {
				return
			}
			a = a[:d1]
		}
	}
}

// med3 mirrors sort_template.h's ST_MED3, returning the index of the median.
func med3[T any](a []T, i, j, k int, cmp func(a, b *T) int) int {
	if cmp(&a[i], &a[j]) < 0 {
		if cmp(&a[j], &a[k]) < 0 {
			return j
		}
		if cmp(&a[i], &a[k]) < 0 {
			return k
		}
		return i
	}
	if cmp(&a[j], &a[k]) > 0 {
		return j
	}
	if cmp(&a[i], &a[k]) < 0 {
		return i
	}
	return k
}

// swapN mirrors ST_SWAPN: swaps the n elements starting at i with those
// starting at j.
func swapN[T any](a []T, i, j, n int) {
	for x := 0; x < n; x++ {
		a[i+x], a[j+x] = a[j+x], a[i+x]
	}
}
