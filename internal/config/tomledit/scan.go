package tomledit

import (
	"bytes"
	"fmt"
	"slices"

	"github.com/pelletier/go-toml/v2/unstable"
)

// document is where a file's tables and keys are.
type document struct {
	tables []table
	values []keyValue
}

// table is a [table] or [[array]] header.
type table struct {
	keys  []string
	start int // where the header's line starts
	end   int // just after the header's line
	array bool
}

// keyValue is one key = value expression.
type keyValue struct {
	keys  []string // the full path, the table's keys first
	local []string // the keys as written
	start int      // where the key starts
	eq    int      // the '='
	end   int      // just after the value
	table int      // index into tables, -1 for the root
}

// scan finds the tables and keys of data.
func scan(data []byte) (document, error) {
	var d document
	p := unstable.Parser{}
	p.Reset(data)
	current := -1
	for p.NextExpression() {
		e := p.Expression()
		switch e.Kind {
		case unstable.Table, unstable.ArrayTable:
			keys, first, last := keysOf(e)
			d.tables = append(d.tables, table{
				keys: keys, start: bytes.LastIndexByte(data[:first], '\n') + 1,
				end: lineEnd(data, last), array: e.Kind == unstable.ArrayTable,
			})
			current = len(d.tables) - 1
		case unstable.KeyValue:
			local, first, last := keysOf(e)
			full := local
			if current >= 0 {
				full = slices.Concat(d.tables[current].keys, local)
			}
			d.values = append(d.values, keyValue{
				keys: full, local: local, start: first, eq: last + bytes.IndexByte(data[last:], '='),
				end: int(e.Raw.Offset + e.Raw.Length), table: current,
			})
		}
	}
	if err := p.Error(); err != nil {
		return document{}, fmt.Errorf("failed to parse the configuration: %w", err)
	}

	return d, nil
}

// keysOf returns an expression's keys, where the first starts, and where
// the last ends.
func keysOf(e *unstable.Node) (keys []string, first, last int) {
	first = -1
	for it := e.Key(); it.Next(); {
		k := it.Node()
		if first < 0 {
			first = int(k.Raw.Offset)
		}
		last = int(k.Raw.Offset + k.Raw.Length)
		keys = append(keys, string(k.Data))
	}

	return keys, first, last
}

// find is the key's own line, outside arrays of tables.
func (d document) find(key []string) (keyValue, bool) {
	for _, kv := range d.values {
		if slices.Equal(kv.keys, key) && (kv.table < 0 || !d.tables[kv.table].array) {
			return kv, true
		}
	}

	return keyValue{}, false
}

// blocked reports whether the key cannot get a line of its own: a value
// such as an inline table holds it, or a table of that name exists.
func (d document) blocked(key []string) bool {
	for _, kv := range d.values {
		if len(kv.keys) < len(key) && slices.Equal(kv.keys, key[:len(kv.keys)]) {
			return true
		}
	}

	return slices.ContainsFunc(d.tables, func(t table) bool { return slices.Equal(t.keys, key) })
}

// insertAt is where a new key of the table goes, and the key as written
// there: after the table's last key (or its header), or, for a table that
// only dotted root keys make ("tui.details = true"), after the last of them
// in the same dotted form. ok is false when the table does not exist.
func (d document) insertAt(data []byte, tableKeys []string, leaf string) (at int, written []string, ok bool) {
	written = []string{leaf}
	if len(tableKeys) == 0 {
		return d.rootInsert(data), written, true
	}
	idx := slices.IndexFunc(d.tables, func(t table) bool { return !t.array && slices.Equal(t.keys, tableKeys) })
	if idx >= 0 {
		at = d.tables[idx].end
		for _, kv := range d.values {
			if kv.table == idx {
				at = lineEnd(data, kv.end)
			}
		}

		return at, written, true
	}
	for _, kv := range d.values {
		if kv.table < 0 && len(kv.keys) > len(tableKeys) && slices.Equal(kv.keys[:len(tableKeys)], tableKeys) {
			at, ok = lineEnd(data, kv.end), true
		}
	}

	return at, append(slices.Clone(tableKeys), leaf), ok
}

// rootInsert is where a new root key goes: after the last root key, else
// before the first table and the comments above it, else before the first
// block `uah config update` added (config.NewSetting.Marker), whose
// commented table would take the key once uncommented, else at the end.
func (d document) rootInsert(data []byte) int {
	at := -1
	for _, kv := range d.values {
		if kv.table < 0 {
			at = lineEnd(data, kv.end)
		}
	}
	switch {
	case at >= 0:
		return min(at, d.updateBlock(data, 0)) // a root key someone put below the block
	case len(d.tables) > 0:
		return trimTail(data, d.tables[0].start)
	}

	return d.updateBlock(data, 0)
}

// updateBlock is where the first line at or after from that begins a block
// `uah config update` added starts, else the end of data; a line inside a
// value, such as a multiline string, is the value's.
func (d document) updateBlock(data []byte, from int) int {
	for start := from; start < len(data); start = lineEnd(data, start) {
		line := bytes.TrimLeft(bytes.TrimPrefix(data[start:], []byte("\uFEFF")), " \t") // indented, or after a BOM, as the update matches it
		if bytes.HasPrefix(line, []byte(updateMarker)) && !d.inValue(start) {
			return start
		}
	}

	return len(data)
}

// inValue reports whether off lies within a key's value.
func (d document) inValue(off int) bool {
	for _, kv := range d.values {
		if off > kv.start && off < kv.end {
			return true
		}
	}

	return false
}

// updateMarker begins each block `uah config update` adds.
const updateMarker = "# uah config update: ["

// CommentLines returns where each line of data starts that is exactly
// comment, indented or after a BOM, and not text inside a value, such as a
// multiline string: a comment line, found in one scan.
func CommentLines(data []byte, comment string) ([]int, error) {
	body := bytes.TrimPrefix(data, []byte("\uFEFF")) // the parser takes no BOM; the loader does
	shift := len(data) - len(body)
	doc, err := scan(body)
	if err != nil {
		return nil, err
	}
	var at []int
	for start := 0; start < len(body); start = lineEnd(body, start) {
		if string(bytes.TrimSpace(body[start:lineEnd(body, start)])) == comment && !doc.inValue(start) {
			at = append(at, start+shift)
		}
	}

	return at, nil
}
