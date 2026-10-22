package devstate

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"time"
)

// An entry in the database file: a version byte, a flags byte (1 = a previous value follows, 2 = lc
// is a change seen as it happened - task 95's changed_at; an older binary ignores the bit),
// ts and lc as 8-byte big-endian nanoseconds, the previous value's duration as 8-byte milliseconds,
// the value, then the previous value when the flag says so. A value is a tag byte and its bytes:
// 0 nil, 1 false, 2 true, 3 an int (8 bytes), 4 a float64 (8 bytes), 5 a string (uvarint length
// and bytes), 6 anything else as JSON (the same). The source and the confirmation are not stored:
// what comes back from the file is "restored" and not confirmed, whatever it was.
const entryV1 = 1

const (
	tagNil = iota
	tagFalse
	tagTrue
	tagInt
	tagFloat
	tagString
	tagJSON
)

func encode(en *entry) []byte {
	b := make([]byte, 0, 40)
	flags := byte(0)
	if en.hasPrev {
		flags |= 1
	}
	if en.seen {
		flags |= 2
	}
	b = append(b, entryV1, flags)
	b = binary.BigEndian.AppendUint64(b, uint64(en.ts.UnixNano()))
	b = binary.BigEndian.AppendUint64(b, uint64(en.lc.UnixNano()))
	b = binary.BigEndian.AppendUint64(b, uint64(en.prevFor.Milliseconds()))
	b = appendValue(b, en.value)
	if en.hasPrev {
		b = appendValue(b, en.prev)
	}
	return b
}

func appendValue(b []byte, v any) []byte {
	switch x := v.(type) {
	case nil:
		return append(b, tagNil)
	case bool:
		if x {
			return append(b, tagTrue)
		}
		return append(b, tagFalse)
	case int:
		b = append(b, tagInt)
		return binary.BigEndian.AppendUint64(b, uint64(int64(x)))
	case float64:
		b = append(b, tagFloat)
		return binary.BigEndian.AppendUint64(b, math.Float64bits(x))
	case string:
		b = append(b, tagString)
		b = binary.AppendUvarint(b, uint64(len(x)))
		return append(b, x...)
	}
	raw, err := json.Marshal(v)
	if err != nil {
		return append(b, tagNil)
	}
	b = append(b, tagJSON)
	b = binary.AppendUvarint(b, uint64(len(raw)))
	return append(b, raw...)
}

func decode(b []byte) (*entry, bool) {
	if len(b) < 26 || b[0] != entryV1 {
		return nil, false
	}
	en := &entry{}
	flags := b[1]
	en.ts = time.Unix(0, int64(binary.BigEndian.Uint64(b[2:10])))
	en.lc = time.Unix(0, int64(binary.BigEndian.Uint64(b[10:18])))
	en.prevFor = time.Duration(int64(binary.BigEndian.Uint64(b[18:26]))) * time.Millisecond
	rest := b[26:]
	var ok bool
	if en.value, rest, ok = readValue(rest); !ok {
		return nil, false
	}
	if flags&1 != 0 {
		if en.prev, _, ok = readValue(rest); !ok {
			return nil, false
		}
		en.hasPrev = true
	}
	en.seen = flags&2 != 0
	return en, true
}

func readValue(b []byte) (any, []byte, bool) {
	if len(b) == 0 {
		return nil, nil, false
	}
	switch b[0] {
	case tagNil:
		return nil, b[1:], true
	case tagFalse:
		return false, b[1:], true
	case tagTrue:
		return true, b[1:], true
	case tagInt:
		if len(b) < 9 {
			return nil, nil, false
		}
		return int(int64(binary.BigEndian.Uint64(b[1:9]))), b[9:], true
	case tagFloat:
		if len(b) < 9 {
			return nil, nil, false
		}
		return math.Float64frombits(binary.BigEndian.Uint64(b[1:9])), b[9:], true
	case tagString, tagJSON:
		n, k := binary.Uvarint(b[1:])
		if k <= 0 || uint64(len(b)-1-k) < n {
			return nil, nil, false
		}
		raw := b[1+k : 1+k+int(n)]
		if b[0] == tagString {
			return string(raw), b[1+k+int(n):], true
		}
		var v any
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, nil, false
		}
		return v, b[1+k+int(n):], true
	}
	return nil, nil, false
}
