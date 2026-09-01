// Copyright 2003-2005 Colin Percival. All rights reserved.
// Ported from bsdiff 4.3 (bsdiff.c). See LICENSE in this directory.

// Package bsdiff produces and applies binary patches in the BSDIFF40 format of
// bsdiff 4.3, the format read by the bspatch embedded in expo-updates.
package bsdiff

import (
	"bytes"
	"encoding/binary"
	"errors"
)

const (
	magic      = "BSDIFF40"
	headerSize = 32
	// maxSize keeps suffix array indices within int32 and stops a corrupt
	// header from requesting an absurd allocation.
	maxSize = 1<<30 - 1
)

var errTooLarge = errors.New("bsdiff: input larger than 1 GiB")

// Diff returns a patch that turns old into new.
func Diff(old, new []byte) ([]byte, error) {
	if len(old) > maxSize || len(new) > maxSize {
		return nil, errTooLarge
	}
	return diff(old, new, newKgramIndex(old).search)
}

// DiffReference is bsdiff 4.3 exactly: it finds matches through a suffix array
// of old and emits the same patch as the C tool, several times slower than Diff.
func DiffReference(old, new []byte) ([]byte, error) {
	if len(old) > maxSize || len(new) > maxSize {
		return nil, errTooLarge
	}
	I := make([]int32, len(old)+1)
	V := make([]int32, len(old)+1)
	qsufsort(I, V, old)
	V = nil
	return diff(old, new, func(new []byte, _ int) (int, int) { return search(I, old, new) })
}

// diff is the bsdiff main loop; search returns the position and length of the
// match in old to use for new, given the old position the current alignment
// predicts.
func diff(old, new []byte, search func(new []byte, expect int) (pos, length int)) ([]byte, error) {
	oldsize, newsize := len(old), len(new)

	db := make([]byte, 0, newsize)
	var eb []byte
	var ctrl bytes.Buffer
	var buf [8]byte

	var scan, length, pos, lastscan, lastpos, lastoffset int
	for scan < newsize {
		oldscore := 0

		scan += length
		for scsc := scan; scan < newsize; scan++ {
			pos, length = search(new[scan:], scan+lastoffset)

			for ; scsc < scan+length; scsc++ {
				if scsc+lastoffset < oldsize && old[scsc+lastoffset] == new[scsc] {
					oldscore++
				}
			}

			if (length == oldscore && length != 0) || length > oldscore+8 {
				break
			}

			if scan+lastoffset < oldsize && old[scan+lastoffset] == new[scan] {
				oldscore--
			}
		}

		if length != oldscore || scan == newsize {
			var s, sf, lenf int
			for i := 0; lastscan+i < scan && lastpos+i < oldsize; {
				if old[lastpos+i] == new[lastscan+i] {
					s++
				}
				i++
				if s*2-i > sf*2-lenf {
					sf = s
					lenf = i
				}
			}

			lenb := 0
			if scan < newsize {
				s, sb := 0, 0
				for i := 1; scan >= lastscan+i && pos >= i; i++ {
					if old[pos-i] == new[scan-i] {
						s++
					}
					if s*2-i > sb*2-lenb {
						sb = s
						lenb = i
					}
				}
			}

			if lastscan+lenf > scan-lenb {
				overlap := (lastscan + lenf) - (scan - lenb)
				s, ss, lens := 0, 0, 0
				for i := 0; i < overlap; i++ {
					if new[lastscan+lenf-overlap+i] == old[lastpos+lenf-overlap+i] {
						s++
					}
					if new[scan-lenb+i] == old[pos-lenb+i] {
						s--
					}
					if s > ss {
						ss = s
						lens = i + 1
					}
				}

				lenf += lens - overlap
				lenb -= lens
			}

			for i := 0; i < lenf; i++ {
				db = append(db, new[lastscan+i]-old[lastpos+i])
			}
			eb = append(eb, new[lastscan+lenf:scan-lenb]...)

			offtout(int64(lenf), buf[:])
			ctrl.Write(buf[:])
			offtout(int64((scan-lenb)-(lastscan+lenf)), buf[:])
			ctrl.Write(buf[:])
			offtout(int64((pos-lenb)-(lastpos+lenf)), buf[:])
			ctrl.Write(buf[:])

			lastscan = scan - lenb
			lastpos = pos - lenb
			lastoffset = pos - scan
		}
	}

	ctrlBz, err := compressBzip2(ctrl.Bytes())
	if err != nil {
		return nil, err
	}
	diffBz, err := compressBzip2(db)
	if err != nil {
		return nil, err
	}
	extraBz, err := compressBzip2(eb)
	if err != nil {
		return nil, err
	}

	patch := make([]byte, 0, headerSize+len(ctrlBz)+len(diffBz)+len(extraBz))
	patch = append(patch, magic...)
	var header [24]byte
	offtout(int64(len(ctrlBz)), header[0:8])
	offtout(int64(len(diffBz)), header[8:16])
	offtout(int64(newsize), header[16:24])
	patch = append(patch, header[:]...)
	patch = append(patch, ctrlBz...)
	patch = append(patch, diffBz...)
	patch = append(patch, extraBz...)
	return patch, nil
}

// search returns the position in old of the longest common prefix with new
// and its length, by binary search over the suffix array I.
func search(I []int32, old, new []byte) (pos, length int) {
	st, en := 0, len(old)
	for en-st >= 2 {
		x := st + (en-st)/2
		p := int(I[x])
		n := min(len(old)-p, len(new))
		if bytes.Compare(old[p:p+n], new[:n]) < 0 {
			st = x
		} else {
			en = x
		}
	}

	x := matchlen(old[I[st]:], new)
	y := matchlen(old[I[en]:], new)
	if x > y {
		return int(I[st]), x
	}
	return int(I[en]), y
}

func matchlen(a, b []byte) int {
	n := min(len(a), len(b))
	i := 0
	for ; i+8 <= n; i += 8 {
		if binary.LittleEndian.Uint64(a[i:]) != binary.LittleEndian.Uint64(b[i:]) {
			break
		}
	}
	for ; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

// offtout writes x as bsdiff's 8 byte sign-magnitude little-endian integer.
func offtout(x int64, buf []byte) {
	y := x
	if x < 0 {
		y = -x
	}
	for i := 0; i < 8; i++ {
		buf[i] = byte(y)
		y >>= 8
	}
	if x < 0 {
		buf[7] |= 0x80
	}
}
