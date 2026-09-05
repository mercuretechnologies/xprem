// Copyright 2003-2005 Colin Percival. All rights reserved.
// Ported from bsdiff 4.3 (bsdiff.c). See LICENSE in this directory.

package bsdiff

// qsufsort fills I with the suffix array of old (Larsson-Sadakane), using V as
// scratch space. Both slices must have len(old)+1 entries.
func qsufsort(I, V []int32, old []byte) {
	oldsize := int32(len(old))

	var buckets [256]int32
	for _, b := range old {
		buckets[b]++
	}
	for i := 1; i < 256; i++ {
		buckets[i] += buckets[i-1]
	}
	for i := 255; i > 0; i-- {
		buckets[i] = buckets[i-1]
	}
	buckets[0] = 0

	for i := int32(0); i < oldsize; i++ {
		buckets[old[i]]++
		I[buckets[old[i]]] = i
	}
	I[0] = oldsize
	for i := int32(0); i < oldsize; i++ {
		V[i] = buckets[old[i]]
	}
	V[oldsize] = 0
	for i := 1; i < 256; i++ {
		if buckets[i] == buckets[i-1]+1 {
			I[buckets[i]] = -1
		}
	}
	I[0] = -1

	for h := int32(1); I[0] != -(oldsize + 1); h += h {
		var length, i int32
		for i < oldsize+1 {
			if I[i] < 0 {
				length -= I[i]
				i -= I[i]
			} else {
				if length != 0 {
					I[i-length] = -length
				}
				length = V[I[i]] + 1 - i
				split(I, V, i, length, h)
				i += length
				length = 0
			}
		}
		if length != 0 {
			I[i-length] = -length
		}
	}

	for i := int32(0); i < oldsize+1; i++ {
		I[V[i]] = i
	}
}

func split(I, V []int32, start, length, h int32) {
	if length < 16 {
		for k := start; k < start+length; {
			j := int32(1)
			x := V[I[k]+h]
			for i := int32(1); k+i < start+length; i++ {
				if V[I[k+i]+h] < x {
					x = V[I[k+i]+h]
					j = 0
				}
				if V[I[k+i]+h] == x {
					I[k+j], I[k+i] = I[k+i], I[k+j]
					j++
				}
			}
			for i := int32(0); i < j; i++ {
				V[I[k+i]] = k + j - 1
			}
			if j == 1 {
				I[k] = -1
			}
			k += j
		}
		return
	}

	x := V[I[start+length/2]+h]
	var jj, kk int32
	for i := start; i < start+length; i++ {
		if V[I[i]+h] < x {
			jj++
		}
		if V[I[i]+h] == x {
			kk++
		}
	}
	jj += start
	kk += jj

	i := start
	var j, k int32
	for i < jj {
		if V[I[i]+h] < x {
			i++
		} else if V[I[i]+h] == x {
			I[i], I[jj+j] = I[jj+j], I[i]
			j++
		} else {
			I[i], I[kk+k] = I[kk+k], I[i]
			k++
		}
	}

	for jj+j < kk {
		if V[I[jj+j]+h] == x {
			j++
		} else {
			I[jj+j], I[kk+k] = I[kk+k], I[jj+j]
			k++
		}
	}

	if jj > start {
		split(I, V, start, jj-start, h)
	}

	for i := int32(0); i < kk-jj; i++ {
		V[I[jj+i]] = kk - 1
	}
	if jj == kk-1 {
		I[jj] = -1
	}

	if start+length > kk {
		split(I, V, kk, start+length-kk, h)
	}
}
