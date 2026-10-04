// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package symbolication

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type sourceReadCounter struct {
	*bytes.Reader
	textsOffset int64
	textReads   int
}

func (r *sourceReadCounter) ReadAt(p []byte, offset int64) (int, error) {
	if offset >= r.textsOffset {
		r.textReads++
	}
	return r.Reader.ReadAt(p, offset)
}

func TestSymbolicateSkipsOversizedSourceBeforeReading(t *testing.T) {
	m := labMap(strings.Repeat("x", maxContextSourceBytes+1), 1)
	var encoded bytes.Buffer
	require.NoError(t, WriteIndex(&encoded, m))
	reader := &sourceReadCounter{Reader: bytes.NewReader(encoded.Bytes()), textsOffset: int64(le.Uint32(encoded.Bytes()[28:]))}
	index, err := OpenIndex(reader)
	require.NoError(t, err)

	trace := Symbolicate(index, hermesTrace())
	require.NotNil(t, trace.Frames[0].Origin)
	assert.Equal(t, "src/LabScreen.tsx", trace.Frames[0].Origin.Source)
	assert.Nil(t, trace.Frames[0].Origin.Context)
	assert.Zero(t, reader.textReads, "the oversized source must be rejected before ReaderAt")
}

func TestSourceTextBudgetIncludesExactBoundary(t *testing.T) {
	index := openIndexOf(t, labMap("12345678", 1))
	text, err := index.sourceText(0, 8)
	require.NoError(t, err)
	assert.Equal(t, "12345678", text)

	_, err = index.sourceText(0, 7)
	assert.ErrorIs(t, err, ErrInvalidIndex)
	// The public method still reads sources using its existing index budget.
	text, err = index.SourceText(0)
	require.NoError(t, err)
	assert.Equal(t, "12345678", text)
}
