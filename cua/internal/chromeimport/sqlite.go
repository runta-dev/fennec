package chromeimport

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"
	"strings"
)

// A minimal read-only SQLite reader: enough to scan the rows of a table
// (b-tree table pages, overflow chains, records) in a database file and
// the committed frames of its write-ahead log. Chrome's Cookies database
// is read from a private copy, so a running Chrome is never disturbed and
// no SQLite library is needed.

type sqliteDB struct {
	data     []byte
	pageSize int
	usable   int
	wal      map[uint32][]byte // committed pages from the WAL
}

func openSQLite(path string) (*sqliteDB, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if len(data) < 100 || string(data[:16]) != "SQLite format 3\x00" {
		return nil, errors.New("not a SQLite database")
	}
	ps := int(binary.BigEndian.Uint16(data[16:18]))
	if ps == 1 {
		ps = 65536
	}
	if ps < 512 || ps&(ps-1) != 0 {
		return nil, errors.New("bad SQLite page size")
	}
	if enc := binary.BigEndian.Uint32(data[56:60]); enc > 1 {
		return nil, errors.New("only UTF-8 databases are read")
	}
	db := &sqliteDB{data: data, pageSize: ps, usable: ps - int(data[20])}
	if wal, err := os.ReadFile(path + "-wal"); err == nil && len(wal) >= 32 {
		db.wal = readWAL(wal, ps)
	}
	return db, nil
}

// readWAL returns the pages of the last committed transaction state: the
// latest copy of each page among the frames up to the last valid commit.
func readWAL(wal []byte, pageSize int) map[uint32][]byte {
	magic := binary.BigEndian.Uint32(wal[0:4])
	if magic != 0x377f0682 && magic != 0x377f0683 {
		return nil
	}
	if int(binary.BigEndian.Uint32(wal[8:12])) != pageSize {
		return nil
	}
	var order binary.ByteOrder = binary.LittleEndian
	if magic == 0x377f0683 {
		order = binary.BigEndian
	}
	sum := func(s0, s1 uint32, b []byte) (uint32, uint32) {
		for i := 0; i+8 <= len(b); i += 8 {
			s0 += order.Uint32(b[i:]) + s1
			s1 += order.Uint32(b[i+4:]) + s0
		}
		return s0, s1
	}
	s0, s1 := sum(0, 0, wal[:24])
	if s0 != binary.BigEndian.Uint32(wal[24:28]) || s1 != binary.BigEndian.Uint32(wal[28:32]) {
		return nil
	}
	salt1, salt2 := binary.BigEndian.Uint32(wal[16:20]), binary.BigEndian.Uint32(wal[20:24])
	committed := map[uint32][]byte{}
	pending := map[uint32][]byte{}
	for off := 32; off+24+pageSize <= len(wal); off += 24 + pageSize {
		h := wal[off : off+24]
		if binary.BigEndian.Uint32(h[8:12]) != salt1 || binary.BigEndian.Uint32(h[12:16]) != salt2 {
			break
		}
		page := wal[off+24 : off+24+pageSize]
		s0, s1 = sum(s0, s1, h[:8])
		s0, s1 = sum(s0, s1, page)
		if s0 != binary.BigEndian.Uint32(h[16:20]) || s1 != binary.BigEndian.Uint32(h[20:24]) {
			break
		}
		pending[binary.BigEndian.Uint32(h[0:4])] = page
		if binary.BigEndian.Uint32(h[4:8]) != 0 { // a commit frame
			for k, v := range pending {
				committed[k] = v
			}
			pending = map[uint32][]byte{}
		}
	}
	return committed
}

func (db *sqliteDB) page(n uint32) ([]byte, error) {
	if p, ok := db.wal[n]; ok {
		return p, nil
	}
	off := int(n-1) * db.pageSize
	if n == 0 || off+db.pageSize > len(db.data) {
		return nil, fmt.Errorf("page %d is outside the database", n)
	}
	return db.data[off : off+db.pageSize], nil
}

func varint(b []byte) (uint64, int) {
	var v uint64
	for i := 0; i < 9 && i < len(b); i++ {
		if i == 8 {
			return v<<8 | uint64(b[i]), 9
		}
		v = v<<7 | uint64(b[i]&0x7f)
		if b[i]&0x80 == 0 {
			return v, i + 1
		}
	}
	return v, len(b)
}

// scan calls fn with every row (rowid and record bytes) of the table
// b-tree rooted at root.
func (db *sqliteDB) scan(root uint32, fn func(rowid int64, rec []byte) error) error {
	seen := map[uint32]bool{}
	var walk func(n uint32) error
	walk = func(n uint32) error {
		if seen[n] {
			return errors.New("a b-tree page loops")
		}
		seen[n] = true
		p, err := db.page(n)
		if err != nil {
			return err
		}
		hdr := 0
		if n == 1 {
			hdr = 100
		}
		typ := p[hdr]
		cells := int(binary.BigEndian.Uint16(p[hdr+3:]))
		switch typ {
		case 0x05: // interior table page
			ptrs := hdr + 12
			for i := 0; i < cells; i++ {
				c := int(binary.BigEndian.Uint16(p[ptrs+2*i:]))
				if err := walk(binary.BigEndian.Uint32(p[c:])); err != nil {
					return err
				}
			}
			return walk(binary.BigEndian.Uint32(p[hdr+8:]))
		case 0x0d: // leaf table page
			ptrs := hdr + 8
			for i := 0; i < cells; i++ {
				c := int(binary.BigEndian.Uint16(p[ptrs+2*i:]))
				size, k := varint(p[c:])
				rowid, k2 := varint(p[c+k:])
				rec, err := db.payload(p, c+k+k2, int(size))
				if err != nil {
					return err
				}
				if err := fn(int64(rowid), rec); err != nil {
					return err
				}
			}
			return nil
		}
		return fmt.Errorf("page %d is not a table b-tree page", n)
	}
	return walk(root)
}

// payload assembles a cell's payload, following its overflow chain.
func (db *sqliteDB) payload(p []byte, at, size int) ([]byte, error) {
	u := db.usable
	x := u - 35
	local := size
	if size > x {
		m := (u-12)*32/255 - 23
		k := m + (size-m)%(u-4)
		local = m
		if k <= x {
			local = k
		}
	}
	if at+local > len(p) {
		return nil, errors.New("a cell runs off its page")
	}
	out := make([]byte, 0, size)
	out = append(out, p[at:at+local]...)
	if local == size {
		return out, nil
	}
	if at+local+4 > len(p) {
		return nil, errors.New("a cell runs off its page")
	}
	next := binary.BigEndian.Uint32(p[at+local:])
	for len(out) < size && next != 0 {
		op, err := db.page(next)
		if err != nil {
			return nil, err
		}
		n := min(size-len(out), u-4)
		out = append(out, op[4:4+n]...)
		next = binary.BigEndian.Uint32(op[:4])
	}
	if len(out) != size {
		return nil, errors.New("an overflow chain ends early")
	}
	return out, nil
}

// decodeRecord reads a record's values: nil, int64, float64, string or
// []byte.
func decodeRecord(rec []byte) ([]any, error) {
	hsize, k := varint(rec)
	if int(hsize) > len(rec) || hsize == 0 {
		return nil, errors.New("bad record header")
	}
	var types []uint64
	for i := k; i < int(hsize); {
		t, n := varint(rec[i:])
		types = append(types, t)
		i += n
	}
	body := rec[hsize:]
	out := make([]any, 0, len(types))
	for _, t := range types {
		var n int
		var v any
		switch {
		case t == 0:
			v = nil
		case t >= 1 && t <= 6:
			n = []int{0, 1, 2, 3, 4, 6, 8}[t]
			if len(body) < n {
				return nil, errors.New("record body is short")
			}
			var x int64
			for i := 0; i < n; i++ {
				x = x<<8 | int64(body[i])
			}
			shift := uint(64 - 8*n)
			v = x << shift >> shift // sign-extend
		case t == 7:
			n = 8
			if len(body) < n {
				return nil, errors.New("record body is short")
			}
			v = math.Float64frombits(binary.BigEndian.Uint64(body))
		case t == 8:
			v = int64(0)
		case t == 9:
			v = int64(1)
		case t >= 12 && t%2 == 0:
			n = int(t-12) / 2
			if len(body) < n {
				return nil, errors.New("record body is short")
			}
			v = bytes.Clone(body[:n])
		case t >= 13:
			n = int(t-13) / 2
			if len(body) < n {
				return nil, errors.New("record body is short")
			}
			v = string(body[:n])
		default:
			return nil, fmt.Errorf("unknown serial type %d", t)
		}
		body = body[n:]
		out = append(out, v)
	}
	return out, nil
}

// table finds a table's root page and column names in sqlite_master.
func (db *sqliteDB) table(name string) (root uint32, cols []string, rowidCol int, err error) {
	rowidCol = -1
	found := false
	err = db.scan(1, func(_ int64, rec []byte) error {
		vals, err := decodeRecord(rec)
		if err != nil || len(vals) < 5 {
			return err
		}
		if vals[0] != "table" || vals[1] != name {
			return nil
		}
		r, _ := vals[3].(int64)
		sql, _ := vals[4].(string)
		root = uint32(r)
		cols, rowidCol = columns(sql)
		found = true
		return nil
	})
	if err == nil && !found {
		err = fmt.Errorf("no table %s", name)
	}
	return root, cols, rowidCol, err
}

// columns reads column names from a CREATE TABLE statement, and which one
// (if any) is an INTEGER PRIMARY KEY, stored as the rowid.
func columns(sql string) ([]string, int) {
	open := strings.IndexByte(sql, '(')
	end := strings.LastIndexByte(sql, ')')
	if open < 0 || end <= open {
		return nil, -1
	}
	var parts []string
	depth, start := 0, open+1
	for i := open + 1; i < end; i++ {
		switch sql[i] {
		case '(':
			depth++
		case ')':
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, sql[start:i])
				start = i + 1
			}
		}
	}
	parts = append(parts, sql[start:end])
	var cols []string
	rowid := -1
	for _, p := range parts {
		f := strings.Fields(strings.TrimSpace(p))
		if len(f) == 0 {
			continue
		}
		switch strings.ToUpper(f[0]) {
		case "UNIQUE", "PRIMARY", "CHECK", "FOREIGN", "CONSTRAINT":
			continue
		}
		up := strings.ToUpper(strings.Join(f, " "))
		if len(f) > 1 && strings.HasPrefix(strings.ToUpper(f[1]), "INTEGER") && strings.Contains(up, "PRIMARY KEY") {
			rowid = len(cols)
		}
		cols = append(cols, strings.Trim(f[0], "`\"[]"))
	}
	return cols, rowid
}

// rows reads every row of a table as column name to value.
func (db *sqliteDB) rows(name string, fn func(map[string]any) error) error {
	root, cols, rowidCol, err := db.table(name)
	if err != nil {
		return err
	}
	return db.scan(root, func(rowid int64, rec []byte) error {
		vals, err := decodeRecord(rec)
		if err != nil {
			return err
		}
		row := make(map[string]any, len(cols))
		for i, c := range cols {
			if i < len(vals) {
				row[c] = vals[i]
			}
		}
		if rowidCol >= 0 {
			row[cols[rowidCol]] = rowid
		}
		return fn(row)
	})
}
