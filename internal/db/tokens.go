package db

import "math"

// UncachedInput folds cache-read and cache-creation tokens out of a
// client-reported input figure, with saturating arithmetic and a floor at
// zero. Single shared definition (R3 review A1-F2): the live receiver, the
// archive migration and the recompute tool must fold cache usage
// identically, or the same request reprices differently depending on which
// path wrote it. Saturating subtraction because hostile exporter values
// (input=1, caches=MaxInt64) must not wrap positive and defeat the floor.
func UncachedInput(input, cacheRead, cacheCreate int64) int64 {
	v := satSub(input, cacheRead)
	v = satSub(v, cacheCreate)
	if v < 0 {
		return 0
	}
	return v
}

func satAdd(a, b int64) int64 {
	if a > 0 && b > math.MaxInt64-a {
		return math.MaxInt64
	}
	if a < 0 && b < math.MinInt64-a {
		return math.MinInt64
	}
	return a + b
}

func satSub(a, b int64) int64 {
	if b == math.MinInt64 {
		return math.MaxInt64
	}
	return satAdd(a, -b)
}
