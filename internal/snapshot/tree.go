package snapshot

import (
	"crypto/sha1"
	"encoding/hex"
	"hash"
	"strconv"
	"strings"

	"github.com/libingtong/ballast/internal/model"
)

// emptyBlobID is the sha1 object id of the empty blob.
var emptyBlobID = [20]byte{
	0xe6, 0x9d, 0xe2, 0x9b, 0xb2, 0xd1, 0xd6, 0x43, 0x4b, 0x8b,
	0x29, 0xae, 0x77, 0x5a, 0xd8, 0xc2, 0xe4, 0x8c, 0x53, 0x91,
}

// hashBlob returns the git object id of a blob with the given content.
func hashBlob(content []byte) [20]byte {
	h := sha1.New()
	writeObjectHeader(h, "blob", int64(len(content)))
	h.Write(content)
	var out [20]byte
	h.Sum(out[:0])
	return out
}

func writeObjectHeader(h hash.Hash, typ string, size int64) {
	var buf [32]byte
	b := append(buf[:0], typ...)
	b = append(b, ' ')
	b = strconv.AppendInt(b, size, 10)
	b = append(b, 0)
	h.Write(b)
}

func modeString(mode uint32) string {
	switch mode {
	case modeRegular:
		return "100644 "
	case modeExec:
		return "100755 "
	case modeSymlink:
		return "120000 "
	case modeGitlink:
		return "160000 "
	}
	return strconv.FormatUint(uint64(mode), 8) + " "
}

// treeID writes the tree object of the (sorted) index entries in memory and
// returns the root tree id, exactly like `git write-tree` on that index.
func treeID(ents []IndexEntry) model.TreeID {
	sum := buildTree(ents, 0)
	return model.TreeID(hex.EncodeToString(sum[:]))
}

// buildTree hashes the tree for ents whose paths share the first prefixLen
// bytes (a directory prefix ending in '/', or empty). ents are in index
// order, so every directory is one contiguous run.
func buildTree(ents []IndexEntry, prefixLen int) [20]byte {
	body := make([]byte, 0, min(len(ents), 1<<14)*40)
	for i := 0; i < len(ents); {
		rest := ents[i].Path[prefixLen:]
		slash := strings.IndexByte(rest, '/')
		if slash < 0 {
			body = append(body, modeString(ents[i].Mode)...)
			body = append(body, rest...)
			body = append(body, 0)
			body = append(body, ents[i].SHA[:]...)
			i++
			continue
		}
		dir := rest[:slash]
		end := prefixLen + slash + 1 // length of "<prefix><dir>/"
		j := i + 1
		for j < len(ents) && len(ents[j].Path) >= end && ents[j].Path[prefixLen:end-1] == dir && ents[j].Path[end-1] == '/' {
			j++
		}
		sub := buildTree(ents[i:j], end)
		body = append(body, "40000 "...)
		body = append(body, dir...)
		body = append(body, 0)
		body = append(body, sub[:]...)
		i = j
	}
	h := sha1.New()
	writeObjectHeader(h, "tree", int64(len(body)))
	h.Write(body)
	var out [20]byte
	h.Sum(out[:0])
	return out
}
