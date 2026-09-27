// Copyright (c) 2026 Axel Marciano (Mercure Technologies). All rights reserved.
// This file is governed by the Mercure Technologies Enterprise Edition License
// (see ee/LICENSE); it is NOT covered by the MIT license of this repository.

package symbolication

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"sort"
)

// The index is one file, in this order:
//
//	header    48 bytes: counts, and where the segments and texts start
//	tables    sources, names, ignore flags, Hermes function offsets, fences,
//	          text spans; read once
//	segments  24 bytes each, sorted by (line, column); read from one fence to
//	          the next
//	texts     the source files back to back; read one file at a time

const (
	indexMagic   = "XSMI"
	indexVersion = uint32(1)
	// headerSize leaves 8 bytes free after the 40 in use, so a field can be
	// added without moving the tables.
	headerSize  = 48
	segmentSize = 24
	// fenceStride is how many segments lie between two fences.
	fenceStride = 1024
)

// ErrInvalidIndex reports an index file the reader cannot use.
var ErrInvalidIndex = errors.New("invalid source map index")

// le writes every number on 4 bytes, smallest byte first: 2 is 02 00 00 00.
var le = binary.LittleEndian

// header is the start of the index: how much of each table there is, and
// where the segments and the texts begin.
type header struct {
	SegmentCount  uint32
	FunctionCount uint32
	FenceCount    uint32
	// SourcesBytes and NamesBytes are sizes in bytes, since a name can have
	// any length; the size of every other table follows from a count.
	SourcesBytes   uint32
	NamesBytes     uint32
	SegmentsOffset uint32
	Lines          uint32
	TextsOffset    uint32
}

// fields lists the header values in their order in the file, after the magic
// and the version.
func (h *header) fields() []*uint32 {
	return []*uint32{&h.SegmentCount, &h.FunctionCount, &h.FenceCount, &h.SourcesBytes, &h.NamesBytes, &h.SegmentsOffset, &h.Lines, &h.TextsOffset}
}

// encode returns the fixed-size index header, including its format version.
func (h header) encode() []byte {
	buf := make([]byte, 0, headerSize)
	buf = append(buf, indexMagic...)
	buf = le.AppendUint32(buf, indexVersion)
	for _, field := range h.fields() {
		buf = le.AppendUint32(buf, *field)
	}
	return append(buf, make([]byte, headerSize-len(buf))...)
}

// decodeHeader reads a header from at least 40 bytes. It returns
// ErrInvalidIndex for an unrecognized format or a segments offset inside
// the header; a shorter buffer may panic.
func decodeHeader(b []byte) (header, error) {
	if string(b[:4]) != indexMagic {
		return header{}, fmt.Errorf("%w: bad magic", ErrInvalidIndex)
	}
	if version := le.Uint32(b[4:]); version != indexVersion {
		return header{}, fmt.Errorf("%w: version %d, expected %d", ErrInvalidIndex, version, indexVersion)
	}
	var h header
	for i, field := range h.fields() {
		*field = le.Uint32(b[8+4*i:])
	}
	if h.SegmentsOffset < headerSize {
		return header{}, fmt.Errorf("%w: segments offset inside the header", ErrInvalidIndex)
	}
	return h, nil
}

// fence is the position of every fenceStride-th segment: a lookup reads the
// segments from one fence to the next.
type fence struct{ line, column uint32 }

// span locates one source text in the texts section.
type span struct{ offset, length uint32 }

// WriteIndex writes the index of m to w. Unsorted segments or a nonempty
// SourcesContent list whose length differs from Sources return ErrInvalidMap.
// Writer errors propagate and may leave a partial index in w.
func WriteIndex(w io.Writer, m *Map) error {
	if !sort.SliceIsSorted(m.Segments, func(i, j int) bool { return segmentLess(m.Segments[i], m.Segments[j]) }) {
		return fmt.Errorf("%w: segments are not sorted", ErrInvalidMap)
	}
	if len(m.SourcesContent) != 0 && len(m.SourcesContent) != len(m.Sources) {
		return fmt.Errorf("%w: %d sources but %d contents", ErrInvalidMap, len(m.Sources), len(m.SourcesContent))
	}
	sources := encodeStrings(m.Sources)
	names := encodeStrings(m.Names)
	tables := encodeTables(m, sources, names)
	segmentsOffset := headerSize + len(tables)
	h := header{
		SegmentCount:   uint32(len(m.Segments)),
		FunctionCount:  uint32(len(m.FunctionOffsets)),
		FenceCount:     uint32(len(fencesOf(m.Segments))),
		SourcesBytes:   uint32(len(sources)),
		NamesBytes:     uint32(len(names)),
		SegmentsOffset: uint32(segmentsOffset),
		Lines:          uint32(m.Lines),
		TextsOffset:    uint32(segmentsOffset + segmentSize*len(m.Segments)),
	}
	if _, err := w.Write(append(h.encode(), tables...)); err != nil {
		return err
	}
	if err := writeSegments(w, m.Segments); err != nil {
		return err
	}
	for _, content := range m.SourcesContent {
		if _, err := io.WriteString(w, content); err != nil {
			return err
		}
	}
	return nil
}

// encodeTables writes the tables in the order OpenIndex reads them.
func encodeTables(m *Map, sources, names []byte) []byte {
	var buf []byte
	buf = append(buf, sources...)
	buf = append(buf, names...)
	// One byte per source, in the same order: 1 when the file is not the
	// app's own code.
	for _, ignored := range m.Ignored {
		if ignored {
			buf = append(buf, 1)
		} else {
			buf = append(buf, 0)
		}
	}
	for _, offset := range m.FunctionOffsets {
		buf = le.AppendUint32(buf, offset)
	}
	// Fences are the table of contents of the segments: fence i is where
	// segment i*fenceStride starts, so a lookup knows which 1024 segments to read.
	for _, f := range fencesOf(m.Segments) {
		buf = le.AppendUint32(buf, f.line)
		buf = le.AppendUint32(buf, f.column)
	}
	// Where each source's content sits in the texts section. The sources table
	// above holds the file names; the contents come last, after the segments,
	// so opening the index never reads them.
	for _, s := range textSpans(m) {
		buf = le.AppendUint32(buf, s.offset)
		buf = le.AppendUint32(buf, s.length)
	}
	return buf
}

// fencesOf returns the generated position at each fenceStride boundary.
func fencesOf(segments []Segment) []fence {
	var fences []fence
	for i := 0; i < len(segments); i += fenceStride {
		fences = append(fences, fence{segments[i].Line, segments[i].Column})
	}
	return fences
}

// textSpans computes where each source text will sit in the texts section,
// each right after the previous one. It writes nothing: the texts are written
// after the segments.
func textSpans(m *Map) []span {
	spans := make([]span, len(m.Sources))
	offset := uint32(0)
	for i := range m.Sources {
		if i < len(m.SourcesContent) {
			spans[i] = span{offset, uint32(len(m.SourcesContent[i]))}
		} else {
			spans[i] = span{offset, 0}
		}
		offset += spans[i].length
	}
	return spans
}

// writeSegments writes the binary segment records, returning the first writer
// error; earlier records may already have been written.
func writeSegments(w io.Writer, segments []Segment) error {
	for first := 0; first < len(segments); first += fenceStride {
		last := min(first+fenceStride, len(segments))
		buf := make([]byte, 0, segmentSize*(last-first))
		for _, s := range segments[first:last] {
			buf = appendSegment(buf, s)
		}
		if _, err := w.Write(buf); err != nil {
			return err
		}
	}
	return nil
}

// appendSegment appends one binary segment record to buf.
func appendSegment(buf []byte, s Segment) []byte {
	for _, value := range []uint32{s.Line, s.Column, s.Source, s.OriginalLine, s.OriginalColumn, s.Name} {
		buf = le.AppendUint32(buf, value)
	}
	return buf
}

// decodeSegment reads one binary segment record; b must contain at least
// segmentSize bytes or the read panics.
func decodeSegment(b []byte) Segment {
	return Segment{
		Line:           le.Uint32(b[0:]),
		Column:         le.Uint32(b[4:]),
		Source:         le.Uint32(b[8:]),
		OriginalLine:   le.Uint32(b[12:]),
		OriginalColumn: le.Uint32(b[16:]),
		Name:           le.Uint32(b[20:]),
	}
}

// segmentLess orders segments by generated line, then column.
func segmentLess(a, b Segment) bool {
	if a.Line != b.Line {
		return a.Line < b.Line
	}
	return a.Column < b.Column
}

// encodeStrings writes 4 bytes for the number of values, then 4 bytes per
// value for where it starts plus 4 for where the last one ends, then the
// values back to back.
func encodeStrings(values []string) []byte {
	buf := le.AppendUint32(nil, uint32(len(values)))
	offset := uint32(0)
	for _, v := range values {
		buf = le.AppendUint32(buf, offset)
		offset += uint32(len(v))
	}
	// A value ends where the next one starts, so the last one needs its own end.
	buf = le.AppendUint32(buf, offset)
	for _, v := range values {
		buf = append(buf, v...)
	}
	return buf
}

// decodeStrings reads an index string table, returning ErrInvalidIndex for
// truncated tables or string offsets outside the supplied data.
func decodeStrings(b []byte) ([]string, error) {
	// Even an empty table is 8 bytes: the count, then the end position.
	if len(b) < 8 {
		return nil, fmt.Errorf("%w: truncated string table", ErrInvalidIndex)
	}
	count := int(le.Uint32(b))    // Read the first 4 bytes of b (words count)
	offsetsEnd := 4 + 4*count + 4 // 4 first bytes = words count, then 4 bytes per words position and 4 bytes for the end
	if count < 0 || len(b) < offsetsEnd {
		return nil, fmt.Errorf("%w: truncated string table", ErrInvalidIndex)
	}
	data := b[offsetsEnd:]
	values := make([]string, count)
	for i := range values {
		// A value ends where the next one starts.
		start, end := le.Uint32(b[4+4*i:]), le.Uint32(b[4+4*(i+1):])
		if start > end || int(end) > len(data) {
			return nil, fmt.Errorf("%w: string table offsets out of range", ErrInvalidIndex)
		}
		values[i] = string(data[start:end])
	}
	return values, nil
}

// tableReader reads the tables one after the other; the first error sticks
// and every later read returns nothing.
type tableReader struct {
	rest []byte
	err  error
}

// take consumes n bytes, or returns nil and records ErrInvalidIndex when n
// is negative or exceeds the remaining data. After an error it consumes nothing.
func (t *tableReader) take(n int) []byte {
	if t.err != nil {
		return nil
	}
	if n < 0 || len(t.rest) < n {
		t.err = fmt.Errorf("%w: truncated tables", ErrInvalidIndex)
		return nil
	}
	out := t.rest[:n]
	t.rest = t.rest[n:]
	return out
}

// strings consumes a string table of size bytes, returning nil and retaining
// the error if reading or decoding fails.
func (t *tableReader) strings(size uint32) []string {
	raw := t.take(int(size))
	if t.err != nil {
		return nil
	}
	values, err := decodeStrings(raw)
	t.err = err
	return values
}

// flags consumes count bytes as flags, with only 1 meaning true. A read
// failure leaves the error in t and returns an empty slice.
func (t *tableReader) flags(count int) []bool {
	raw := t.take(count)
	flags := make([]bool, len(raw))
	for i, b := range raw {
		flags[i] = b == 1
	}
	return flags
}

// uint32s consumes count little-endian values. A read failure leaves the
// error in t and returns an empty slice.
func (t *tableReader) uint32s(count uint32) []uint32 {
	raw := t.take(4 * int(count))
	values := make([]uint32, len(raw)/4)
	for i := range values {
		values[i] = le.Uint32(raw[4*i:])
	}
	return values
}

// pairs reads count pairs of uint32, the shape of both fences and spans.
func (t *tableReader) pairs(count int) [][2]uint32 {
	raw := t.take(8 * count)
	pairs := make([][2]uint32, len(raw)/8)
	for i := range pairs {
		pairs[i] = [2]uint32{le.Uint32(raw[8*i:]), le.Uint32(raw[8*i+4:])}
	}
	return pairs
}

// ReadSegmentCount reads how many segments an index holds from its header
// alone. Read and header validation failures return ErrInvalidIndex.
func ReadSegmentCount(r io.Reader) (int, error) {
	head := make([]byte, headerSize)
	if _, err := io.ReadFull(r, head); err != nil {
		return 0, fmt.Errorf("%w: %v", ErrInvalidIndex, err)
	}
	h, err := decodeHeader(head)
	if err != nil {
		return 0, err
	}
	return int(h.SegmentCount), nil
}

// Index is an opened index: the tables in memory, the segments read on demand.
type Index struct {
	reader          io.ReaderAt
	h               header
	Sources         []string
	Names           []string
	Ignored         []bool
	functionOffsets []uint32
	fences          []fence
	texts           []span
}

// OpenIndex reads the header and the tables of an index. r must stay open
// while the index is used. Read and format failures return ErrInvalidIndex;
// segment records and source text are not read or validated here.
func OpenIndex(r io.ReaderAt) (*Index, error) {
	head := make([]byte, headerSize)
	if _, err := r.ReadAt(head, 0); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidIndex, err)
	}
	h, err := decodeHeader(head)
	if err != nil {
		return nil, err
	}
	tables := make([]byte, h.SegmentsOffset-headerSize)
	if _, err := r.ReadAt(tables, headerSize); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidIndex, err)
	}

	t := &tableReader{rest: tables}
	x := &Index{reader: r, h: h}
	x.Sources = t.strings(h.SourcesBytes)
	x.Names = t.strings(h.NamesBytes)
	x.Ignored = t.flags(len(x.Sources))
	x.functionOffsets = t.uint32s(h.FunctionCount)
	for _, p := range t.pairs(int(h.FenceCount)) {
		x.fences = append(x.fences, fence{line: p[0], column: p[1]})
	}
	for _, p := range t.pairs(len(x.Sources)) {
		x.texts = append(x.texts, span{offset: p[0], length: p[1]})
	}
	if t.err != nil {
		return nil, t.err
	}
	if len(t.rest) != 0 {
		return nil, fmt.Errorf("%w: %d unexpected bytes before the segments", ErrInvalidIndex, len(t.rest))
	}
	return x, nil
}

// SegmentCount is how many segments the index holds.
func (x *Index) SegmentCount() int { return int(x.h.SegmentCount) }

// SourceText reads the text at a zero-based source index; "" when the map
// carried none. An out-of-range source returns ErrInvalidIndex; underlying
// read errors are wrapped and returned.
func (x *Index) SourceText(source int) (string, error) {
	if source < 0 || source >= len(x.texts) {
		return "", fmt.Errorf("%w: source %d out of range", ErrInvalidIndex, source)
	}
	s := x.texts[source]
	if s.length == 0 {
		return "", nil
	}
	buf := make([]byte, s.length)
	if _, err := x.reader.ReadAt(buf, int64(x.h.TextsOffset)+int64(s.offset)); err != nil {
		return "", fmt.Errorf("reading source %d: %w", source, err)
	}
	return string(buf), nil
}

// Position is where a generated position comes from.
type Position struct {
	Source string
	// SourceIndex is Source's place in the map, the argument SourceText takes.
	SourceIndex int
	// Line and Column are one-based, as editors count them.
	Line    int
	Column  int
	Name    string
	Ignored bool
}

// Lookup resolves a generated position, zero-based. ok is false when nothing
// maps there: before the first segment on that line, or a segment with no
// valid source. Returned positions are one-based. Segment read errors propagate.
func (x *Index) Lookup(line, column uint32) (pos Position, ok bool, err error) {
	target := Segment{Line: line, Column: column}
	fenceIndex, ok := x.fenceBefore(target)
	if !ok {
		return Position{}, false, nil
	}
	segments, err := x.readAfterFence(fenceIndex)
	if err != nil {
		return Position{}, false, err
	}
	segment, ok := lastAtOrBefore(segments, target)
	if !ok || segment.Line != line || segment.Source == NoIndex || int(segment.Source) >= len(x.Sources) {
		return Position{}, false, nil
	}
	return x.position(segment), true, nil
}

// fenceBefore finds the last fence not past target: the segment target
// falls in lies between it and the next fence.
func (x *Index) fenceBefore(target Segment) (int, bool) {
	fenceIndex := sort.Search(len(x.fences), func(i int) bool {
		return segmentLess(target, Segment{Line: x.fences[i].line, Column: x.fences[i].column})
	}) - 1
	return fenceIndex, fenceIndex >= 0
}

// readAfterFence reads the segments from a fence up to the next one.
func (x *Index) readAfterFence(fenceIndex int) ([]Segment, error) {
	first := fenceIndex * fenceStride
	count := min(fenceStride, int(x.h.SegmentCount)-first)
	buf := make([]byte, count*segmentSize)
	if _, err := x.reader.ReadAt(buf, int64(x.h.SegmentsOffset)+int64(first)*segmentSize); err != nil {
		return nil, fmt.Errorf("reading the segments after fence %d: %w", fenceIndex, err)
	}
	segments := make([]Segment, count)
	for i := range segments {
		segments[i] = decodeSegment(buf[i*segmentSize:])
	}
	return segments, nil
}

// lastAtOrBefore is the segment a generated position falls in.
func lastAtOrBefore(segments []Segment, target Segment) (Segment, bool) {
	i := sort.Search(len(segments), func(i int) bool { return segmentLess(target, segments[i]) }) - 1
	if i < 0 {
		return Segment{}, false
	}
	return segments[i], true
}

// position turns a segment's numbers into names.
func (x *Index) position(s Segment) Position {
	pos := Position{
		Source:      x.Sources[s.Source],
		SourceIndex: int(s.Source),
		Line:        int(s.OriginalLine) + 1,
		Column:      int(s.OriginalColumn) + 1,
		Ignored:     x.Ignored[s.Source],
	}
	if s.Name != NoIndex && int(s.Name) < len(x.Names) {
		pos.Name = x.Names[s.Name]
	}
	return pos
}

// LookupHermesFunction resolves a Hermes frame given as a function id and a
// bytecode offset inside it, the minidump form; a plain virtual offset is
// Lookup(0, offset). An unknown function id returns ok=false without error;
// lookup read errors propagate.
func (x *Index) LookupHermesFunction(functionID, localOffset uint32) (Position, bool, error) {
	if int(functionID) >= len(x.functionOffsets) {
		return Position{}, false, nil
	}
	return x.Lookup(0, x.functionOffsets[functionID]+localOffset)
}
