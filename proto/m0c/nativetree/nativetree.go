// Package nativetree 是 M0c 的实验:不启动 git 子进程,直接解析 .git/index(v2/v3),
// 按 exempt glob 过滤后计算与 `git write-tree` 相同的 tree id(只算哈希,不写对象库)。
//
// 仅覆盖"普通 commit 的 C"(不含 add -u 的工作区读取)。遇到无法保证等价的情形
// (v4 索引、扩展标志、未合并条目、sparse 目录、非 SHA-1 等)返回 ErrFallback,调用方应退回 git。
package nativetree

import (
	"bytes"
	"crypto/sha1"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strconv"

	"github.com/bmatcuk/doublestar/v4"
)

// ErrFallback 表示本实现无法保证与 git 等价,应退回 git 子进程。
var ErrFallback = errors.New("nativetree: fallback to git")

type entry struct {
	path string
	mode uint32
	sha  [20]byte
}

func parseIndex(b []byte) ([]entry, error) {
	if len(b) < 12+20 || string(b[:4]) != "DIRC" {
		return nil, ErrFallback
	}
	ver := binary.BigEndian.Uint32(b[4:8])
	if ver != 2 && ver != 3 {
		return nil, ErrFallback
	}
	n := int(binary.BigEndian.Uint32(b[8:12]))
	ents := make([]entry, 0, n)
	off := 12
	for i := 0; i < n; i++ {
		if off+62 > len(b) {
			return nil, ErrFallback
		}
		e := entry{mode: binary.BigEndian.Uint32(b[off+24 : off+28])}
		copy(e.sha[:], b[off+40:off+60])
		flags := binary.BigEndian.Uint16(b[off+60 : off+62])
		hdr := 62
		if flags&0x4000 != 0 { // 扩展标志(skip-worktree / intent-to-add)
			return nil, ErrFallback
		}
		if flags&0x3000 != 0 { // stage != 0:未合并
			return nil, ErrFallback
		}
		if e.mode == 0o040000 { // sparse 目录条目
			return nil, ErrFallback
		}
		end := bytes.IndexByte(b[off+hdr:], 0)
		if end < 0 {
			return nil, ErrFallback
		}
		e.path = string(b[off+hdr : off+hdr+end])
		entLen := hdr + end + 1
		entLen = (entLen + 7) &^ 7
		off += entLen
		ents = append(ents, e)
	}
	// 扩展区:遇到 link(split index)或 sdir(sparse)就退回;其余(TREE/REUC/UNTR/FSMN…)可忽略。
	for off+8+20 <= len(b)-0 {
		sig := string(b[off : off+4])
		sz := int(binary.BigEndian.Uint32(b[off+4 : off+8]))
		if off+8+sz > len(b)-20 {
			break
		}
		if sig == "link" || sig == "sdir" {
			return nil, ErrFallback
		}
		off += 8 + sz
	}
	return ents, nil
}

func matchAny(globs []string, p string) bool {
	for _, g := range globs {
		if ok, _ := doublestar.Match(g, p); ok {
			return true
		}
	}
	return false
}

// TreeID 返回过滤(移除匹配 exempt 的条目)后的 tree id,等价于
// `git rm --cached -r -q --ignore-unmatch -- :(glob)<exempt>... && git write-tree`。
func TreeID(indexPath string, exempt []string) (string, error) {
	b, err := os.ReadFile(indexPath)
	if err != nil {
		return "", err
	}
	all, err := parseIndex(b)
	if err != nil {
		return "", err
	}
	ents := all[:0]
	for _, e := range all {
		if !matchAny(exempt, e.path) {
			ents = append(ents, e)
		}
	}
	sha, _ := buildTree(ents, 0)
	return fmt.Sprintf("%x", sha), nil
}

// buildTree 对 ents(同一目录前缀 depth 之后的路径,索引按字节序排列)递归构造 tree。
func buildTree(ents []entry, prefixLen int) ([20]byte, int) {
	var body bytes.Buffer
	i := 0
	for i < len(ents) {
		rest := ents[i].path[prefixLen:]
		slash := indexByte(rest, '/')
		if slash < 0 {
			body.WriteString(strconv.FormatUint(uint64(ents[i].mode), 8))
			body.WriteByte(' ')
			body.WriteString(rest)
			body.WriteByte(0)
			body.Write(ents[i].sha[:])
			i++
			continue
		}
		dir := rest[:slash]
		j := i
		for j < len(ents) {
			r := ents[j].path[prefixLen:]
			if len(r) > slash && r[:slash] == dir && r[slash] == '/' {
				j++
				continue
			}
			break
		}
		sub, _ := buildTree(ents[i:j], prefixLen+slash+1)
		body.WriteString("40000 ")
		body.WriteString(dir)
		body.WriteByte(0)
		body.Write(sub[:])
		i = j
	}
	h := sha1.New()
	fmt.Fprintf(h, "tree %d\x00", body.Len())
	h.Write(body.Bytes())
	var out [20]byte
	copy(out[:], h.Sum(nil))
	return out, i
}

func indexByte(s string, c byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == c {
			return i
		}
	}
	return -1
}
