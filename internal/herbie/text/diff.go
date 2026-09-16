// SPDX-License-Identifier: MIT
package text

import (
	"math"
	"strconv"
)

const (
	contextLines   = 3
	windowSlack    = 64
	regionStepsMin = 64
	regionStepsMax = 1024
	workLimit      = 1 << 28
)

type diffSide struct {
	data      []byte
	off, hash []uint32
}

func (s *diffSide) count() int        { return len(s.hash) }
func (s *diffSide) line(i int) []byte { return s.data[s.off[i]:s.off[i+1]] }

func lineHash(line []byte) uint32 {
	h := uint32(2166136261)
	for _, c := range line {
		h = (h ^ uint32(c)) * 16777619
	}
	return h
}
func newDiffSide(data []byte) diffSide {
	count := 0
	for _, c := range data {
		if c == '\n' {
			count++
		}
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		count++
	}
	s := diffSide{data: data, off: make([]uint32, count+1), hash: make([]uint32, count)}
	at := 1
	for i := 0; i+1 < len(data); i++ {
		if data[i] == '\n' {
			s.off[at] = uint32(i + 1)
			at++
		}
	}
	s.off[count] = uint32(len(data))
	for i := range s.hash {
		s.hash[i] = lineHash(s.line(i))
	}
	return s
}
func linesEqual(x diffSide, i int, y diffSide, j int) bool {
	xl, yl := x.line(i), y.line(j)
	if x.hash[i] != y.hash[j] || len(xl) != len(yl) {
		return false
	}
	for i := range xl {
		if xl[i] != yl[i] {
			return false
		}
	}
	return true
}
func lineBlank(s diffSide, i int) bool {
	for _, c := range s.line(i) {
		if c != ' ' && c != '\t' && c != '\r' && c != '\n' {
			return false
		}
	}
	return true
}

type diffWindow struct{ lo, aHi, bHi, base int }

func unifiedWindow(a, b []byte) diffWindow {
	common := min(len(a), len(b))
	p := 0
	for p < common && a[p] == b[p] {
		p++
	}
	lo := p
	for lo > 0 && a[lo-1] != '\n' {
		lo--
	}
	for range windowSlack {
		if lo == 0 {
			break
		}
		lo--
		for lo > 0 && a[lo-1] != '\n' {
			lo--
		}
	}
	w := diffWindow{lo: lo}
	for _, c := range a[:lo] {
		if c == '\n' {
			w.base++
		}
	}
	s, maxSuffix := 0, common-lo
	for s < maxSuffix && a[len(a)-1-s] == b[len(b)-1-s] {
		s++
	}
	if s == 0 {
		w.aHi, w.bHi = len(a), len(b)
		return w
	}
	nl := -1
	for i, c := range a[len(a)-s:] {
		if c == '\n' {
			nl = len(a) - s + i
			break
		}
	}
	if nl < 0 {
		w.aHi, w.bHi = len(a), len(b)
		return w
	}
	hi := nl + 1
	for range windowSlack {
		if hi == len(a) {
			break
		}
		end := hi
		for end < len(a) && a[end] != '\n' {
			end++
		}
		if end == len(a) {
			hi = end
		} else {
			hi = end + 1
		}
	}
	w.aHi, w.bHi = hi, len(b)-(len(a)-hi)
	return w
}

type diffContext struct {
	a, b               diffSide
	aChanged, bChanged []bool
	kvf, kvb           []int
	work               int
}

func (c *diffContext) equal(i, j int) bool {
	if c.work == 0 {
		return false
	}
	c.work--
	return linesEqual(c.a, i, c.b, j)
}
func (c *diffContext) equalRev(aHi, bHi, x, y int) bool { return c.equal(aHi-1-x, bHi-1-y) }
func splitOut(aLo, bLo, n, m, x, y int) (int, int) {
	return aLo + max(0, min(n, x)), bLo + max(0, min(m, y))
}
func (c *diffContext) myersSplit(aLo, aHi, bLo, bHi int) (int, int) {
	n, m := aHi-aLo, bHi-bLo
	delta := n - m
	odd := delta&1 != 0
	off := regionStepsMax + 1
	budget := regionStepsMin
	for budget < regionStepsMax && budget*budget < n+m {
		budget *= 2
	}
	budget = min(budget, (n+m)/2+1)
	best, bestX, bestY := -1, 0, 0
	c.kvf[off+1], c.kvb[off+1] = 0, 0
	for d := 0; d <= budget && c.work > 0; d++ {
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && c.kvf[off+k-1] < c.kvf[off+k+1]) {
				x = c.kvf[off+k+1]
			} else {
				x = c.kvf[off+k-1] + 1
			}
			y := x - k
			for x < n && y < m && c.equal(aLo+x, bLo+y) {
				x++
				y++
			}
			c.kvf[off+k] = x
			if x <= n && y <= m && x+y > best {
				best, bestX, bestY = x+y, x, y
			}
			kr := delta - k
			if odd && kr >= -(d-1) && kr <= d-1 && x >= n-c.kvb[off+kr] {
				return splitOut(aLo, bLo, n, m, x, y)
			}
		}
		for k := -d; k <= d; k += 2 {
			var x int
			if k == -d || (k != d && c.kvb[off+k-1] < c.kvb[off+k+1]) {
				x = c.kvb[off+k+1]
			} else {
				x = c.kvb[off+k-1] + 1
			}
			y := x - k
			for x < n && y < m && c.equalRev(aHi, bHi, x, y) {
				x++
				y++
			}
			c.kvb[off+k] = x
			kf := delta - k
			if !odd && kf >= -d && kf <= d && c.kvf[off+kf] >= n-x {
				return splitOut(aLo, bLo, n, m, n-x, m-y)
			}
		}
	}
	return splitOut(aLo, bLo, n, m, bestX, bestY)
}
func mark(changed []bool, lo, hi int) {
	for i := lo; i < hi; i++ {
		changed[i] = true
	}
}
func (c *diffContext) region(aLo, aHi, bLo, bHi int) {
	for {
		for aLo < aHi && bLo < bHi && c.equal(aLo, bLo) {
			aLo++
			bLo++
		}
		for aHi > aLo && bHi > bLo && c.equal(aHi-1, bHi-1) {
			aHi--
			bHi--
		}
		if aLo == aHi {
			mark(c.bChanged, bLo, bHi)
			return
		}
		if bLo == bHi {
			mark(c.aChanged, aLo, aHi)
			return
		}
		sa, sb := aLo, bLo
		degenerate := true
		if c.work > 0 {
			sa, sb = c.myersSplit(aLo, aHi, bLo, bHi)
			degenerate = (sa == aLo && sb == bLo) || (sa == aHi && sb == bHi)
		}
		if degenerate {
			mark(c.aChanged, aLo, aHi)
			mark(c.bChanged, bLo, bHi)
			return
		}
		if (sa-aLo)+(sb-bLo) <= (aHi-sa)+(bHi-sb) {
			c.region(aLo, sa, bLo, sb)
			aLo, bLo = sa, sb
		} else {
			c.region(sa, aHi, sb, bHi)
			aHi, bHi = sa, sb
		}
	}
}
func runScore(s diffSide, start, end int) int {
	score := 0
	if start == 0 || lineBlank(s, start-1) {
		score += 2
	}
	if end == s.count() || lineBlank(s, end-1) {
		score++
	}
	return score
}
func slideRuns(s diffSide, changed []bool) {
	for scan := 0; scan < s.count(); {
		if !changed[scan] {
			scan++
			continue
		}
		start, end := scan, scan
		for end < s.count() && changed[end] {
			end++
		}
		run := end - start
		lo := start
		for lo > 0 && !changed[lo-1] && linesEqual(s, lo-1, s, lo+run-1) {
			lo--
		}
		hi := start
		for hi+run < s.count() && !changed[hi+run] && linesEqual(s, hi, s, hi+run) {
			hi++
		}
		best, score := lo, -1
		for pos := lo; pos <= hi; pos++ {
			if n := runScore(s, pos, pos+run); n >= score {
				best, score = pos, n
			}
		}
		if best != start {
			for i := start; i < end; i++ {
				changed[i] = false
			}
			mark(changed, best, best+run)
		}
		scan = max(best, start) + run
	}
}

type diffChange struct{ aStart, aLines, bStart, bLines int }

func changes(a, b []bool) (out []diffChange) {
	for i, j := 0, 0; i < len(a) || j < len(b); {
		if (i < len(a) && a[i]) || (j < len(b) && b[j]) {
			c := diffChange{aStart: i, bStart: j}
			for i < len(a) && a[i] {
				i++
			}
			for j < len(b) && b[j] {
				j++
			}
			c.aLines, c.bLines = i-c.aStart, j-c.bStart
			out = append(out, c)
		} else {
			i++
			j++
		}
	}
	return
}
func appendLine(out []byte, marker byte, s diffSide, i int) []byte {
	line := s.line(i)
	out = append(out, marker)
	out = append(out, line...)
	if len(line) > 0 && line[len(line)-1] != '\n' {
		out = append(out, "\n\\ No newline at end of file\n"...)
	}
	return out
}
func appendRange(out []byte, start, count int) []byte {
	if count == 1 {
		return strconv.AppendInt(out, int64(start+1), 10)
	}
	if count == 0 {
		out = strconv.AppendInt(out, int64(start), 10)
	} else {
		out = strconv.AppendInt(out, int64(start+1), 10)
	}
	out = append(out, ',')
	return strconv.AppendInt(out, int64(count), 10)
}
func regionLineCount(data []byte) int {
	count := 0
	for _, c := range data {
		if c == '\n' {
			count++
		}
	}
	if len(data) > 0 && data[len(data)-1] != '\n' {
		count++
	}
	return count
}

func appendRegionLines(out []byte, marker byte, data []byte) []byte {
	for len(data) > 0 {
		end := 0
		for end < len(data) && data[end] != '\n' {
			end++
		}
		if end < len(data) {
			end++
		}
		out = append(out, marker)
		out = append(out, data[:end]...)
		if end == len(data) && data[end-1] != '\n' {
			out = append(out, "\n\\ No newline at end of file\n"...)
		}
		data = data[end:]
	}
	return out
}

func appendHunks(out []byte, a, b diffSide, cs []diffChange, base int) []byte {
	for group := 0; group < len(cs); {
		last := group
		for last+1 < len(cs) && cs[last+1].aStart-(cs[last].aStart+cs[last].aLines) <= 2*contextLines {
			last++
		}
		aEnd, bEnd := cs[last].aStart+cs[last].aLines, cs[last].bStart+cs[last].bLines
		aLo := max(0, cs[group].aStart-contextLines)
		aHi := min(a.count(), aEnd+contextLines)
		bLo := cs[group].bStart - (cs[group].aStart - aLo)
		bHi := bEnd + (aHi - aEnd)
		out = append(out, "@@ -"...)
		out = appendRange(out, base+aLo, aHi-aLo)
		out = append(out, " +"...)
		out = appendRange(out, base+bLo, bHi-bLo)
		out = append(out, " @@\n"...)
		i := aLo
		for c := group; c <= last; c++ {
			for ; i < cs[c].aStart; i++ {
				out = appendLine(out, ' ', a, i)
			}
			for ; i < cs[c].aStart+cs[c].aLines; i++ {
				out = appendLine(out, '-', a, i)
			}
			for j := cs[c].bStart; j < cs[c].bStart+cs[c].bLines; j++ {
				out = appendLine(out, '+', b, j)
			}
		}
		for ; i < aHi; i++ {
			out = appendLine(out, ' ', a, i)
		}
		group = last + 1
	}
	return out
}

// UnifiedDiff returns a three-context unified diff, or an empty string for identical inputs.
func UnifiedDiff(a, b []byte, aLabel, bLabel string) string {
	if string(a) == string(b) {
		return ""
	}
	w := unifiedWindow(a, b)
	out := []byte("--- " + aLabel + "\n+++ " + bLabel + "\n")
	if w.aHi-w.lo > math.MaxUint32 || w.bHi-w.lo > math.MaxUint32 {
		out = append(out, "@@ -"...)
		out = appendRange(out, w.base, regionLineCount(a[w.lo:w.aHi]))
		out = append(out, " +"...)
		out = appendRange(out, w.base, regionLineCount(b[w.lo:w.bHi]))
		out = append(out, " @@\n"...)
		out = appendRegionLines(out, '-', a[w.lo:w.aHi])
		return SanitizeUTF8(appendRegionLines(out, '+', b[w.lo:w.bHi]))
	}
	x, y := newDiffSide(a[w.lo:w.aHi]), newDiffSide(b[w.lo:w.bHi])
	c := diffContext{a: x, b: y, aChanged: make([]bool, x.count()), bChanged: make([]bool, y.count()), kvf: make([]int, 2*regionStepsMax+3), kvb: make([]int, 2*regionStepsMax+3), work: workLimit}
	c.region(0, x.count(), 0, y.count())
	slideRuns(x, c.aChanged)
	slideRuns(y, c.bChanged)
	return SanitizeUTF8(appendHunks(out, x, y, changes(c.aChanged, c.bChanged), w.base))
}
