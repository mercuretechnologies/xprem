package bsdiff

import "encoding/binary"

const (
	// kgram is the window hashed into the index and step the sampling interval
	// of old; a match shorter than kgram+step-1 can go unnoticed.
	kgram = 16
	step  = 8
	// maxChain caps the candidates examined per lookup.
	maxChain = 32
)

// kgramIndex maps the hash of every step-th kgram of old to its positions,
// grouped by bucket in increasing order.
type kgramIndex struct {
	old   []byte
	start []int32
	pos   []int32
	shift uint
}

func newKgramIndex(old []byte) *kgramIndex {
	n := 0
	if len(old) >= kgram {
		n = (len(old)-kgram)/step + 1
	}
	bits := uint(8)
	for 1<<bits < n {
		bits++
	}
	x := &kgramIndex{old: old, shift: 64 - bits, start: make([]int32, 1<<bits+1), pos: make([]int32, n)}
	for i := 0; i < n; i++ {
		x.start[hash(old[i*step:])>>x.shift+1]++
	}
	for h := 1; h < len(x.start); h++ {
		x.start[h] += x.start[h-1]
	}
	for i := 0; i < n; i++ {
		h := hash(old[i*step:]) >> x.shift
		x.pos[x.start[h]] = int32(i * step)
		x.start[h]++
	}
	copy(x.start[1:], x.start[:len(x.start)-1])
	x.start[0] = 0
	return x
}

func hash(b []byte) uint64 {
	h := binary.LittleEndian.Uint64(b) * 0x9E3779B97F4A7C15
	if kgram > 8 {
		h ^= binary.LittleEndian.Uint64(b[kgram-8:]) * 0xC2B2AE3D27D4EB4F
		h *= 0x9E3779B97F4A7C15
	}
	return h
}

// search returns the longest match in old for the start of new, among the
// current alignment expect and the indexed candidates nearest to it.
func (x *kgramIndex) search(new []byte, expect int) (pos, length int) {
	old := x.old
	if 0 <= expect && expect < len(old) {
		pos, length = expect, matchlen(old[expect:], new)
	}
	for j := 0; j < step && j+kgram <= len(new); j++ {
		h := hash(new[j:]) >> x.shift
		lo, hi := int(x.start[h]), int(x.start[h+1])
		// Split the bucket at expect+j and walk outward from there.
		want := expect + j
		l, r := lo, hi
		for l < r {
			m := l + (r-l)/2
			if int(x.pos[m]) < want {
				l = m + 1
			} else {
				r = m
			}
		}
		l--
		for n := 0; n < maxChain && (l >= lo || r < hi); n++ {
			var p int
			if r >= hi || (l >= lo && want-int(x.pos[l]) <= int(x.pos[r])-want) {
				p, l = int(x.pos[l]), l-1
			} else {
				p, r = int(x.pos[r]), r+1
			}
			q := p - j
			if q < 0 || q+length > len(old) || (length > 0 && old[q+length-1] != new[length-1]) {
				continue
			}
			if m := matchlen(old[q:], new); m > length || (m == length && abs(q-expect) < abs(pos-expect)) {
				pos, length = q, m
			}
		}
	}
	return pos, length
}

func abs(x int) int {
	if x < 0 {
		return -x
	}
	return x
}
