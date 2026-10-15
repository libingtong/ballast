package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// readInput reads a file, or stdin for "-".
func readInput(name string) ([]byte, error) {
	if name == "-" {
		return io.ReadAll(os.Stdin)
	}
	return os.ReadFile(name)
}

// lookup walks a dotted path (object keys and array indexes).
func lookup(doc any, path string) (any, bool) {
	cur := doc
	if path == "" {
		return cur, true
	}
	for _, seg := range strings.Split(path, ".") {
		switch v := cur.(type) {
		case map[string]any:
			next, ok := v[seg]
			if !ok {
				return nil, false
			}
			cur = next
		case []any:
			i, err := strconv.Atoi(seg)
			if err != nil || i < 0 || i >= len(v) {
				return nil, false
			}
			cur = v[i]
		default:
			return nil, false
		}
	}
	return cur, true
}

// scalarString renders a JSON value the way scripts compare it.
func scalarString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case nil:
		return "null"
	case json.Number:
		return x.String()
	case bool:
		return strconv.FormatBool(x)
	}
	b, _ := json.Marshal(v)
	return string(b)
}

func decodeNumbers(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var doc any
	if err := dec.Decode(&doc); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, fmt.Errorf("trailing data after the JSON value")
	}
	return doc, nil
}

// cmdJSON implements: json get|len|has|append|valid FILE [PATH [VALUE]].
// get prints the value (strings raw) and exits 1 when the path is missing;
// len prints the length of an array/object/string; has exits 0 or 1;
// append adds VALUE (JSON) to the array at PATH in place, creating objects
// and the array as needed; valid exits 0 for a single JSON value.
func cmdJSON(args []string) int {
	if len(args) < 2 {
		return fail("json: want <op> FILE [PATH [VALUE]]")
	}
	op, file := args[0], args[1]
	path := ""
	if len(args) > 2 {
		path = args[2]
	}
	data, err := readInput(file)
	if err != nil {
		return fail("json: %v", err)
	}
	doc, err := decodeNumbers(data)
	switch op {
	case "valid":
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		return 0
	}
	if err != nil {
		return fail("json: %s: %v", file, err)
	}
	switch op {
	case "get", "has":
		v, ok := lookup(doc, path)
		if !ok {
			return 1
		}
		if op == "get" {
			fmt.Println(scalarString(v))
		}
		return 0
	case "len":
		v, ok := lookup(doc, path)
		if !ok {
			return 1
		}
		switch x := v.(type) {
		case []any:
			fmt.Println(len(x))
		case map[string]any:
			fmt.Println(len(x))
		case string:
			fmt.Println(len([]rune(x)))
		default:
			return fail("json: len: %q is neither array, object nor string", path)
		}
		return 0
	case "append":
		if len(args) != 4 || file == "-" {
			return fail("json: append wants FILE PATH VALUE")
		}
		val, verr := decodeNumbers([]byte(args[3]))
		if verr != nil {
			return fail("json: append: VALUE is not JSON: %v", verr)
		}
		root, ok := doc.(map[string]any)
		if !ok {
			return fail("json: append: the document is not an object")
		}
		segs := strings.Split(path, ".")
		cur := root
		for _, s := range segs[:len(segs)-1] {
			next, ok := cur[s].(map[string]any)
			if !ok {
				next = map[string]any{}
				cur[s] = next
			}
			cur = next
		}
		last := segs[len(segs)-1]
		arr, _ := cur[last].([]any)
		cur[last] = append(arr, val)
		out, merr := json.MarshalIndent(root, "", "  ")
		if merr != nil {
			return fail("json: %v", merr)
		}
		if werr := os.WriteFile(file, append(out, '\n'), 0o644); werr != nil {
			return fail("json: %v", werr)
		}
		return 0
	}
	return fail("json: unknown op %q", op)
}

// jsonlFiles lists the *.jsonl files under path (or path itself).
func jsonlFiles(path string) ([]string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return []string{path}, nil
	}
	var out []string
	err = filepath.WalkDir(path, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(d.Name(), ".jsonl") {
			out = append(out, p)
		}
		return nil
	})
	sort.Strings(out)
	return out, err
}

// forEachLine calls fn for every non-empty line of the jsonl files.
func forEachLine(files []string, fn func(file string, no int, line []byte)) error {
	for _, f := range files {
		fh, err := os.Open(f)
		if err != nil {
			return err
		}
		sc := bufio.NewScanner(fh)
		sc.Buffer(make([]byte, 64<<10), 64<<20)
		no := 0
		for sc.Scan() {
			no++
			if line := bytes.TrimSpace(sc.Bytes()); len(line) > 0 {
				fn(f, no, line)
			}
		}
		err = sc.Err()
		_ = fh.Close()
		if err != nil {
			return err
		}
	}
	return nil
}

// cmdJSONL implements: jsonl count PATH [path=value ...] (prints how many
// records match every condition; a missing PATH counts as zero) and
// jsonl valid PATH (every line must be one JSON object; prints the count).
func cmdJSONL(args []string) int {
	if len(args) < 2 {
		return fail("jsonl: want <op> PATH [path=value ...]")
	}
	op, root := args[0], args[1]
	files, err := jsonlFiles(root)
	if err != nil {
		if os.IsNotExist(err) && op == "count" {
			fmt.Println(0)
			return 0
		}
		return fail("jsonl: %v", err)
	}
	type cond struct{ path, want string }
	var conds []cond
	for _, a := range args[2:] {
		k, v, ok := strings.Cut(a, "=")
		if !ok {
			return fail("jsonl: condition %q is not path=value", a)
		}
		conds = append(conds, cond{k, v})
	}
	total, matched, bad := 0, 0, 0
	rerr := forEachLine(files, func(file string, no int, line []byte) {
		total++
		doc, derr := decodeNumbers(line)
		obj, isObj := doc.(map[string]any)
		if derr != nil || !isObj {
			bad++
			fmt.Fprintf(os.Stderr, "%s:%d: not a JSON object\n", file, no)
			return
		}
		for _, c := range conds {
			v, ok := lookup(obj, c.path)
			if !ok || scalarString(v) != c.want {
				return
			}
		}
		matched++
	})
	if rerr != nil {
		return fail("jsonl: %v", rerr)
	}
	switch op {
	case "count":
		fmt.Println(matched)
	case "valid":
		fmt.Println(total)
		if bad > 0 {
			return 1
		}
	default:
		return fail("jsonl: unknown op %q", op)
	}
	return 0
}
