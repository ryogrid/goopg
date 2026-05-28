// hash_partition.go — Jenkins Bob hash and hash_combine64 for satisfies_hash_partition.
// Mirrors postgres/src/common/hashfn.c and postgres/src/include/common/hashfn.h.
// M0097-0027.
package executor

import "encoding/binary"

const hashPartitionSeed uint64 = 0x7A5B22367996DCFD

// pgHashCombine64 is hash_combine64 from hashfn.h.
func pgHashCombine64(a, b uint64) uint64 {
	a ^= b + 0x49a0f4dd15e5a8e3 + (a << 54) + (a >> 7)
	return a
}

// pgHashMix is the Jenkins mix step used in hash_bytes_extended.
func pgHashMix(a, b, c *uint32) {
	*a -= *c
	*a ^= (*c<<4 | *c>>28)
	*c += *b
	*b -= *a
	*b ^= (*a<<6 | *a>>26)
	*a += *c
	*c -= *b
	*c ^= (*b<<8 | *b>>24)
	*b += *a
	*a -= *c
	*a ^= (*c<<16 | *c>>16)
	*c += *b
	*b -= *a
	*b ^= (*a<<19 | *a>>13)
	*a += *c
	*c -= *b
	*c ^= (*b<<4 | *b>>28)
	*b += *a
}

// pgHashFinal is the Jenkins final avalanche step.
func pgHashFinal(a, b, c *uint32) {
	*c ^= *b
	*c -= (*b<<14 | *b>>18)
	*a ^= *c
	*a -= (*c<<11 | *c>>21)
	*b ^= *a
	*b -= (*a<<25 | *a>>7)
	*c ^= *b
	*c -= (*b<<16 | *b>>16)
	*a ^= *c
	*a -= (*c<<4 | *c>>28)
	*b ^= *a
	*b -= (*a<<14 | *a>>18)
	*c ^= *b
	*c -= (*b<<24 | *b>>8)
}

// pgHashBytesExtended is hash_bytes_extended from hashfn.c.
// Returns a 64-bit hash of the byte slice combined with seed.
func pgHashBytesExtended(k []byte, seed uint64) uint64 {
	keylen := uint32(len(k))
	var a, b, c uint32
	a = 0x9e3779b9 + keylen + 3923095
	b = a
	c = a
	if seed != 0 {
		c += uint32(seed >> 32)
		b += uint32(seed)
	}

	for len(k) >= 12 {
		a += binary.LittleEndian.Uint32(k[0:4])
		b += binary.LittleEndian.Uint32(k[4:8])
		c += binary.LittleEndian.Uint32(k[8:12])
		pgHashMix(&a, &b, &c)
		k = k[12:]
	}

	switch len(k) {
	case 11:
		c += uint32(k[10]) << 24
		fallthrough
	case 10:
		c += uint32(k[9]) << 16
		fallthrough
	case 9:
		c += uint32(k[8]) << 8
		fallthrough
	case 8:
		b += uint32(k[7]) << 24
		fallthrough
	case 7:
		b += uint32(k[6]) << 16
		fallthrough
	case 6:
		b += uint32(k[5]) << 8
		fallthrough
	case 5:
		b += uint32(k[4])
		fallthrough
	case 4:
		a += uint32(k[3]) << 24
		fallthrough
	case 3:
		a += uint32(k[2]) << 16
		fallthrough
	case 2:
		a += uint32(k[1]) << 8
		fallthrough
	case 1:
		a += uint32(k[0])
	}

	pgHashFinal(&a, &b, &c)
	return (uint64(b) << 32) | uint64(c)
}

// pgHashUint32Extended is hash_bytes_uint32_extended from hashfn.c.
// Returns a 64-bit hash of a single uint32 combined with seed.
func pgHashUint32Extended(k uint32, seed uint64) uint64 {
	var a, b, c uint32
	a = 0x9e3779b9 + 4 + 3923095
	b = a
	c = a
	if seed != 0 {
		c += uint32(seed >> 32)
		b += uint32(seed)
		pgHashMix(&a, &b, &c)
	}
	a += k
	pgHashFinal(&a, &b, &c)
	return (uint64(b) << 32) | uint64(c)
}
