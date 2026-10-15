package stats

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"sort"

	"github.com/libingtong/ballast/internal/model"
)

// edit replaces data[from:to] with repl.
type edit struct {
	from, to int
	repl     []byte
}

// member is one top-level key of a rule object.
type member struct {
	key              string
	keyStart, keyEnd int
	valStart, valEnd int
}

// RewriteRuleActions returns the rule file with the "action" of rule id set
// to action and, when the rule has an explicit "target" equal to its old
// action, "target" set to action too. Only those string values change: the
// file is edited at token level (key order and every other value stay as
// written) and the result is re-indented with two spaces.
func RewriteRuleActions(data []byte, id string, action model.Action) ([]byte, error) {
	rules, err := scanRules(data)
	if err != nil {
		return nil, err
	}
	var obj *ruleObj
	for i := range rules {
		if rules[i].id == id {
			obj = &rules[i]
			break
		}
	}
	if obj == nil {
		return nil, fmt.Errorf("stats: rule %q not found in file", id)
	}

	var (
		actionM, targetM, eventM *member
		oldAction                = model.ActionLog
	)
	for i := range obj.members {
		switch m := &obj.members[i]; m.key {
		case "action":
			actionM = m
		case "target":
			targetM = m
		case "event":
			eventM = m
		}
	}
	quoted, _ := json.Marshal(string(action))

	var edits []edit
	if actionM != nil {
		var cur string
		if err := json.Unmarshal(data[actionM.valStart:actionM.valEnd], &cur); err != nil {
			return nil, fmt.Errorf("stats: rule %q has a non-string action", id)
		}
		if cur != "" {
			oldAction = model.Action(cur)
		}
		edits = append(edits, edit{actionM.valStart, actionM.valEnd, quoted})
	} else {
		// Insert before "target"/"event" when present, else append.
		pos, lead := obj.closeBrace, ","
		first := len(obj.members) == 0
		switch {
		case targetM != nil:
			pos = targetM.keyStart
		case eventM != nil:
			pos = eventM.keyStart
		}
		ins := append([]byte(`"action": `), quoted...)
		if pos == obj.closeBrace {
			if first {
				lead = ""
			}
			edits = append(edits, edit{pos, pos, append([]byte(lead), ins...)})
		} else {
			edits = append(edits, edit{pos, pos, append(ins, ',')})
		}
	}
	if targetM != nil {
		var cur string
		if err := json.Unmarshal(data[targetM.valStart:targetM.valEnd], &cur); err == nil && model.Action(cur) == oldAction {
			edits = append(edits, edit{targetM.valStart, targetM.valEnd, quoted})
		}
	}

	sort.Slice(edits, func(i, j int) bool { return edits[i].from > edits[j].from })
	out := append([]byte(nil), data...)
	for _, e := range edits {
		out = append(out[:e.from], append(append([]byte(nil), e.repl...), out[e.to:]...)...)
	}

	var buf bytes.Buffer
	if err := json.Indent(&buf, out, "", "  "); err != nil {
		return nil, fmt.Errorf("stats: re-encoding rule file: %w", err)
	}
	return append(bytes.TrimRight(buf.Bytes(), " \t\r\n"), '\n'), nil
}

type ruleObj struct {
	id         string
	members    []member
	closeBrace int
}

// scanRules walks {"rules":[{...},...]} with a json.Decoder and records the
// byte ranges of every member of every rule object.
func scanRules(data []byte) ([]ruleObj, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	tok, err := dec.Token()
	if err != nil {
		return nil, fmt.Errorf("stats: parsing rule file: %w", err)
	}
	if d, ok := tok.(json.Delim); !ok || d != '{' {
		return nil, errors.New("stats: rule file is not a JSON object")
	}
	var out []ruleObj
	for dec.More() {
		kt, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("stats: parsing rule file: %w", err)
		}
		key, _ := kt.(string)
		if key != "rules" {
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return nil, fmt.Errorf("stats: parsing rule file: %w", err)
			}
			continue
		}
		at, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("stats: parsing rule file: %w", err)
		}
		if d, ok := at.(json.Delim); !ok || d != '[' {
			return nil, errors.New("stats: \"rules\" is not an array")
		}
		for dec.More() {
			ot, err := dec.Token()
			if err != nil {
				return nil, fmt.Errorf("stats: parsing rule file: %w", err)
			}
			if d, ok := ot.(json.Delim); !ok || d != '{' {
				return nil, errors.New("stats: rule is not an object")
			}
			var ro ruleObj
			for dec.More() {
				mk, err := dec.Token()
				if err != nil {
					return nil, fmt.Errorf("stats: parsing rule file: %w", err)
				}
				name, _ := mk.(string)
				keyEnd := int(dec.InputOffset())
				var raw json.RawMessage
				if err := dec.Decode(&raw); err != nil {
					return nil, fmt.Errorf("stats: parsing rule file: %w", err)
				}
				valEnd := int(dec.InputOffset())
				m := member{key: name, keyStart: keyStart(data, keyEnd), keyEnd: keyEnd, valStart: valEnd - len(raw), valEnd: valEnd}
				ro.members = append(ro.members, m)
				if name == "id" {
					_ = json.Unmarshal(raw, &ro.id)
				}
			}
			if _, err := dec.Token(); err != nil { // closing '}'
				return nil, fmt.Errorf("stats: parsing rule file: %w", err)
			}
			ro.closeBrace = int(dec.InputOffset()) - 1
			out = append(out, ro)
		}
		if _, err := dec.Token(); err != nil { // closing ']'
			return nil, fmt.Errorf("stats: parsing rule file: %w", err)
		}
	}
	return out, nil
}

// keyStart finds the opening quote of the JSON string ending at keyEnd.
func keyStart(data []byte, keyEnd int) int {
	i := keyEnd - 2 // before the closing quote
	for i >= 0 {
		if data[i] == '"' {
			bs := 0
			for j := i - 1; j >= 0 && data[j] == '\\'; j-- {
				bs++
			}
			if bs%2 == 0 {
				return i
			}
		}
		i--
	}
	return 0
}
