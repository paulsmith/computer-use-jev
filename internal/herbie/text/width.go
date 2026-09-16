// SPDX-License-Identifier: MIT
package text

func cellsAt(s string, off int) (int, int) {
	c, n := UTF8CodepointCells([]byte(s), off)
	if c < 0 {
		c = 1
	}
	return c, n
}
func skipZero(s string, off int) int {
	for off < len(s) {
		c, n := cellsAt(s, off)
		if c != 0 {
			break
		}
		off += n
	}
	return off
}
func advanceCells(s string, max int) int {
	off, cells := 0, 0
	for off < len(s) && cells < max {
		c, n := cellsAt(s, off)
		if cells+c > max {
			break
		}
		cells += c
		off += n
	}
	return skipZero(s, off)
}
func DisplayCells(s string) int {
	cells := 0
	for off := 0; off < len(s); {
		if s[off] == '\x1b' && off+1 < len(s) && s[off+1] == '[' {
			i := off + 2
			for i < len(s) && (s[i] < '@' || s[i] > '~') {
				i++
			}
			if i < len(s) {
				off = i + 1
				continue
			}
		}
		c, n := cellsAt(s, off)
		cells += c
		off += n
	}
	return cells
}
func TruncateForDisplay(s string, maxCells int) string {
	if len(s) <= maxCells || advanceCells(s, maxCells) == len(s) {
		return s
	}
	content := maxCells
	if maxCells >= 4 {
		content -= 3
	}
	out := s[:advanceCells(s, content)]
	if maxCells >= 4 {
		out += "..."
	}
	return out
}
func strictBreakPos(s string, max int) (int, int) {
	off, cells, last := 0, 0, -1
	for off < len(s) {
		c, n := cellsAt(s, off)
		if cells+c > max {
			if s[off] == ' ' && cells == max {
				last = off
			}
			break
		}
		if s[off] == ' ' {
			last = off
		}
		cells += c
		off += n
	}
	if off >= len(s) {
		return len(s), len(s)
	}
	if last < 0 {
		end := advanceCells(s, max)
		return end, end
	}
	end := last
	for end > 0 && s[end-1] == ' ' {
		end--
	}
	return end, last + 1
}
func wrapBreakPos(s string, maxCells int) (end, next int) {
	if maxCells < 1 {
		panic("maxCells must be positive")
	}
	end, next = strictBreakPos(s, maxCells)
	if end == 0 && next == 0 && len(s) > 0 {
		end = skipZero(s, utf8Next([]byte(s), 0))
		next = end
	}
	return
}

// WrapRowBytes returns the bytes in the next display row and its following separator.
func WrapRowBytes(s string, maxCells int) (row, separator int) {
	paragraph := s
	for i := range s {
		if s[i] == '\n' {
			paragraph = s[:i]
			break
		}
	}
	row, next := wrapBreakPos(paragraph, maxCells)
	if next == len(paragraph) && len(paragraph) < len(s) {
		next++
	}
	return row, next - row
}

func ReflowForDisplay(s string, firstRowCells, otherRowCells, maxRows, lastRowReserve int) string {
	if maxRows < 1 {
		maxRows = 1
	}
	if firstRowCells < 1 {
		firstRowCells = 1
	}
	if otherRowCells < 1 {
		otherRowCells = 1
	}
	if lastRowReserve < 0 {
		lastRowReserve = 0
	}
	single := max(firstRowCells-lastRowReserve, 1)
	if len(s) <= single {
		return s
	}
	out, off := "", 0
	for row := range maxRows {
		budget := otherRowCells
		if row == 0 {
			budget = firstRowCells
		}
		content := max(budget-lastRowReserve, 1)
		remaining := s[off:]
		if advanceCells(remaining, content) == len(remaining) {
			out += remaining
			break
		}
		if row == maxRows-1 {
			before := content - 3
			if before < 1 {
				out += remaining[:advanceCells(remaining, content)]
			} else {
				end, _ := strictBreakPos(remaining, before)
				out += remaining[:end] + "..."
			}
			break
		}
		end, next := wrapBreakPos(remaining, content)
		out += remaining[:end] + "\n"
		off += next
	}
	return out
}
func FlattenForDisplay(s string) string {
	out := make([]byte, 0, len(s))
	space := true
	zero := 0
	for off := 0; off < len(s); {
		b := s[off]
		if b < 0x80 {
			isSpace := b == ' ' || b == '\t' || b == '\n' || b == '\r' || b < 0x20 || b == 0x7f
			if isSpace {
				if !space {
					out = append(out, ' ')
					space = true
				}
			} else {
				out = append(out, b)
				space = false
			}
			zero = 0
			off++
			continue
		}
		c, n := UTF8CodepointCells([]byte(s), off)
		if c < 0 {
			out = append(out, '?')
			zero = 0
			space = false
		} else if c == 0 {
			if zero < 8 {
				out = append(out, s[off:off+n]...)
				zero++
			}
		} else {
			out = append(out, s[off:off+n]...)
			zero = 0
			space = false
		}
		off += n
	}
	if len(out) > 0 && out[len(out)-1] == ' ' {
		out = out[:len(out)-1]
	}
	return string(out)
}
