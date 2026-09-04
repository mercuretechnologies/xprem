// Copyright 2003-2005 Colin Percival. All rights reserved.
// Ported from bsdiff 4.3 (bspatch.c). See LICENSE in this directory.

package bsdiff

import (
	"bytes"
	"compress/bzip2"
	"errors"
	"io"
	"slices"
)

// ErrCorruptPatch is returned by Patch for anything that is not a well-formed
// BSDIFF40 patch for a file of the announced size.
var ErrCorruptPatch = errors.New("bsdiff: corrupt patch")

// Patch applies a BSDIFF40 patch to old and returns the new file. It does not
// verify that old is the file the patch was made for: the caller must compare
// the result against the expected hash.
func Patch(old, patch []byte) ([]byte, error) {
	if len(patch) < headerSize || string(patch[:8]) != magic {
		return nil, ErrCorruptPatch
	}
	ctrlLen := offtin(patch[8:16])
	diffLen := offtin(patch[16:24])
	newSize := offtin(patch[24:32])
	rest := int64(len(patch) - headerSize)
	if ctrlLen < 0 || diffLen < 0 || newSize < 0 || newSize > maxSize ||
		ctrlLen > rest || diffLen > rest-ctrlLen {
		return nil, ErrCorruptPatch
	}
	diffStart := headerSize + ctrlLen
	extraStart := diffStart + diffLen
	ctrl := bzip2.NewReader(bytes.NewReader(patch[headerSize:diffStart]))
	diff := bzip2.NewReader(bytes.NewReader(patch[diffStart:extraStart]))
	extra := bzip2.NewReader(bytes.NewReader(patch[extraStart:]))

	oldSize := int64(len(old))
	// The output grows as the streams are decoded, so a bogus header cannot
	// force a huge allocation up front.
	out := make([]byte, 0, min(newSize, 1<<20))
	var oldpos int64
	var buf [8]byte
	for int64(len(out)) < newSize {
		var c [3]int64
		for i := range c {
			if _, err := io.ReadFull(ctrl, buf[:]); err != nil {
				return nil, ErrCorruptPatch
			}
			c[i] = offtin(buf[:])
		}

		if c[0] < 0 || c[0] > newSize-int64(len(out)) {
			return nil, ErrCorruptPatch
		}
		newpos := len(out)
		out = slices.Grow(out, int(c[0]))[:newpos+int(c[0])]
		if _, err := io.ReadFull(diff, out[newpos:]); err != nil {
			return nil, ErrCorruptPatch
		}
		for i := int64(0); i < c[0]; i++ {
			if oldpos+i < oldSize {
				out[int64(newpos)+i] += old[oldpos+i]
			}
		}
		oldpos += c[0]

		if c[1] < 0 || c[1] > newSize-int64(len(out)) {
			return nil, ErrCorruptPatch
		}
		newpos = len(out)
		out = slices.Grow(out, int(c[1]))[:newpos+int(c[1])]
		if _, err := io.ReadFull(extra, out[newpos:]); err != nil {
			return nil, ErrCorruptPatch
		}

		// Stricter than bspatch.c, which tolerates seeks outside old: bsdiff
		// never emits them, and rejecting them keeps oldpos from overflowing.
		if c[2] < -oldpos || c[2] > oldSize-oldpos {
			return nil, ErrCorruptPatch
		}
		oldpos += c[2]
	}
	return out, nil
}

// offtin reads bsdiff's 8 byte sign-magnitude little-endian integer.
func offtin(buf []byte) int64 {
	y := int64(buf[7] & 0x7F)
	for i := 6; i >= 0; i-- {
		y = y*256 + int64(buf[i])
	}
	if buf[7]&0x80 != 0 {
		y = -y
	}
	return y
}
