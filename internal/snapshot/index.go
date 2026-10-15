package snapshot

import (
	"bytes"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"time"

	"github.com/libingtong/ballast/internal/model"
)

// Index entry modes the native path understands.
const (
	modeRegular = 0o100644
	modeExec    = 0o100755
	modeSymlink = 0o120000
	modeGitlink = 0o160000
	modeSparse  = 0o040000
)

// Flag bits of IndexEntry.Flags (the 16-bit field of the on-disk entry).
const (
	flagAssumeValid = 0x8000
	flagExtended    = 0x4000
	flagStageMask   = 0x3000
)

// IndexEntry is one parsed index entry (stage-0 only; others ⇒ fallback).
type IndexEntry struct {
	Path    string
	Mode    uint32
	SHA     [20]byte
	CtimeS  uint32
	CtimeNS uint32
	MtimeS  uint32
	MtimeNS uint32
	Dev     uint32
	Ino     uint32
	UID     uint32
	GID     uint32
	Size    uint32
	Flags   uint16
}

// Index is a parsed index file.
type Index struct {
	Version int
	Entries []IndexEntry
	// MtimeNS of the index file itself (racy-git reference).
	FileMtime time.Time
}

func fallbackf(format string, a ...any) error {
	return fmt.Errorf("%w: %s", model.ErrFallback, fmt.Sprintf(format, a...))
}

// ParseIndex parses index versions 2, 3 and 4 (v4 path prefix compression).
// Returns model.ErrFallback (wrapped with a reason) for unsupported content.
//
// The trailing SHA-1 checksum is verified (unless it is all zero, as written
// with index.skipHash). Paths are substrings of one copy of data, so the
// caller may reuse its buffer. FileMtime is left zero; the caller sets it.
func ParseIndex(data []byte) (*Index, error) {
	if len(data) < 12+20 || string(data[:4]) != "DIRC" {
		return nil, fallbackf("not a v2-v4 index")
	}
	ver := binary.BigEndian.Uint32(data[4:8])
	if ver < 2 || ver > 4 {
		return nil, fallbackf("index version %d", ver)
	}
	body := data[:len(data)-20]
	if sum := data[len(data)-20:]; !allZero(sum) {
		if h := sha1.Sum(body); !bytes.Equal(h[:], sum) {
			return nil, fallbackf("index checksum mismatch")
		}
	}
	n := int(binary.BigEndian.Uint32(data[8:12]))
	if n < 0 || n > (len(body)-12)/64 { // every entry takes at least 64 bytes
		return nil, fallbackf("corrupt index: %d entries", n)
	}
	str := string(body)
	idx := &Index{Version: int(ver), Entries: make([]IndexEntry, n)}
	var arena []byte
	var starts []int32 // v4: path boundaries in arena (start of entry i at starts[2i], end at starts[2i+1])
	if ver == 4 {
		arena = make([]byte, 0, len(body))
		starts = make([]int32, 0, 2*n)
	}
	off := 12
	prevStart, prevEnd := 0, 0
	for i := 0; i < n; i++ {
		if off+62 > len(body) {
			return nil, fallbackf("corrupt index: truncated entry %d", i)
		}
		b := body[off:]
		e := &idx.Entries[i]
		e.CtimeS = binary.BigEndian.Uint32(b[0:])
		e.CtimeNS = binary.BigEndian.Uint32(b[4:])
		e.MtimeS = binary.BigEndian.Uint32(b[8:])
		e.MtimeNS = binary.BigEndian.Uint32(b[12:])
		e.Dev = binary.BigEndian.Uint32(b[16:])
		e.Ino = binary.BigEndian.Uint32(b[20:])
		e.Mode = binary.BigEndian.Uint32(b[24:])
		e.UID = binary.BigEndian.Uint32(b[28:])
		e.GID = binary.BigEndian.Uint32(b[32:])
		e.Size = binary.BigEndian.Uint32(b[36:])
		copy(e.SHA[:], b[40:60])
		e.Flags = binary.BigEndian.Uint16(b[60:])
		hdr := 62
		if e.Flags&flagStageMask != 0 {
			return nil, fallbackf("unmerged entry")
		}
		if e.Flags&flagExtended != 0 {
			if ver < 3 || off+64 > len(body) {
				return nil, fallbackf("corrupt index: bad extended flags")
			}
			ext := binary.BigEndian.Uint16(b[62:])
			switch {
			case ext&0x4000 != 0:
				return nil, fallbackf("skip-worktree entry")
			case ext&0x2000 != 0:
				return nil, fallbackf("intent-to-add entry")
			case ext != 0:
				return nil, fallbackf("unknown extended flags %#x", ext)
			}
			hdr = 64
		}
		switch e.Mode {
		case modeRegular, modeExec, modeSymlink, modeGitlink:
		case modeSparse:
			return nil, fallbackf("sparse index directory entry")
		default:
			return nil, fallbackf("unusual entry mode %o", e.Mode)
		}
		pos := off + hdr
		if ver == 4 {
			strip, vn := decodeVarint(body[pos:])
			if vn == 0 || strip > uint64(prevEnd-prevStart) {
				return nil, fallbackf("corrupt index: bad v4 prefix length")
			}
			pos += vn
			z := bytes.IndexByte(body[pos:], 0)
			if z < 0 {
				return nil, fallbackf("corrupt index: unterminated path")
			}
			start := len(arena)
			arena = append(arena, arena[prevStart:prevEnd-int(strip)]...)
			arena = append(arena, body[pos:pos+z]...)
			prevStart, prevEnd = start, len(arena)
			starts = append(starts, int32(start), int32(prevEnd))
			off = pos + z + 1
		} else {
			nameLen := int(e.Flags & 0xfff)
			end := pos + nameLen
			if nameLen == 0xfff {
				z := bytes.IndexByte(body[pos:], 0)
				if z < 0 {
					return nil, fallbackf("corrupt index: unterminated path")
				}
				end = pos + z
			} else if end >= len(body) || body[end] != 0 {
				return nil, fallbackf("corrupt index: bad path length")
			}
			e.Path = str[pos:end]
			off += (hdr + (end - pos) + 8) &^ 7
			if off > len(body) {
				return nil, fallbackf("corrupt index: truncated entry %d", i)
			}
		}
	}
	if ver == 4 {
		astr := string(arena)
		for i := range idx.Entries {
			idx.Entries[i].Path = astr[starts[2*i]:starts[2*i+1]]
		}
	}
	for i := 1; i < len(idx.Entries); i++ {
		if idx.Entries[i-1].Path >= idx.Entries[i].Path {
			return nil, fallbackf("index entries out of order")
		}
	}
	return idx, parseExtensions(body, off, idx)
}

// parseExtensions walks the extension area. Optional extensions (upper-case
// signature) are skipped; split index and sparse index are required ones.
func parseExtensions(body []byte, off int, idx *Index) error {
	for off+8 <= len(body) {
		sig := body[off : off+4]
		size := int(binary.BigEndian.Uint32(body[off+4:]))
		if size < 0 || off+8+size > len(body) {
			return fallbackf("corrupt index: bad extension size")
		}
		switch string(sig) {
		case "link":
			return fallbackf("split index")
		case "sdir":
			return fallbackf("sparse index")
		default:
			if sig[0] < 'A' || sig[0] > 'Z' {
				return fallbackf("required index extension %q", sig)
			}
		}
		off += 8 + size
	}
	if off != len(body) {
		return fallbackf("corrupt index: trailing bytes")
	}
	return nil
}

func allZero(b []byte) bool {
	for _, c := range b {
		if c != 0 {
			return false
		}
	}
	return true
}

// decodeVarint decodes git's offset-style varint (index v4 path prefix
// length); n is 0 on malformed input.
func decodeVarint(b []byte) (v uint64, n int) {
	if len(b) == 0 {
		return 0, 0
	}
	c := b[0]
	v = uint64(c & 127)
	n = 1
	for c&128 != 0 {
		if n >= len(b) || n > 9 {
			return 0, 0
		}
		v++
		c = b[n]
		n++
		v = v<<7 + uint64(c&127)
	}
	return v, n
}
