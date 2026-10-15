package dsl

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"sort"
	"strings"

	"github.com/libingtong/ballast/internal/model"
)

// decodeError is the error type of DecodeRuleFile; it carries the
// diagnostics Lint and ParseFile report for an undecodable file.
type decodeError struct {
	diags []model.Diagnostic
	msg   string
}

func (e *decodeError) Error() string { return e.msg }

// DecodeRuleFile strictly decodes data (unknown fields are errors) without
// semantic validation.
func DecodeRuleFile(data []byte) (*model.RuleFile, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf"))
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	var f model.RuleFile
	if err := dec.Decode(&f); err != nil {
		return nil, classifyDecodeError(err, data)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, newDecodeError(model.Diagnostic{
			Severity: model.SevError, Code: CodeSchema, Message: "unexpected data after the top-level JSON value",
		})
	}
	// encoding/json folds key case and lets a later duplicate win; the
	// schema does neither, so check the raw keys.
	if ds := keyDiagnostics(data); len(ds) > 0 {
		return nil, newDecodeError(ds...)
	}
	return &f, nil
}

func newDecodeError(ds ...model.Diagnostic) *decodeError {
	msgs := make([]string, len(ds))
	for i, d := range ds {
		msgs[i] = d.Message
	}
	return &decodeError{diags: ds, msg: "dsl: " + strings.Join(msgs, "; ")}
}

func classifyDecodeError(err error, data []byte) error {
	var syn *json.SyntaxError
	var typ *json.UnmarshalTypeError
	switch {
	case errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return newDecodeError(model.Diagnostic{Severity: model.SevError, Code: CodeSchema, Message: "invalid JSON: unexpected end of input"})
	case errors.As(err, &syn):
		return newDecodeError(model.Diagnostic{
			Severity: model.SevError, Code: CodeSchema,
			Message: fmt.Sprintf("invalid JSON at offset %d: %s", syn.Offset, syn.Error()),
		})
	case errors.As(err, &typ):
		ptrStr := ""
		if typ.Field != "" {
			ptrStr = "/" + strings.ReplaceAll(typ.Field, ".", "/")
		}
		return newDecodeError(model.Diagnostic{
			Severity: model.SevError, Code: CodeSchema, Pointer: ptrStr,
			Message: fmt.Sprintf("field %q: expected %s, got JSON %s", typ.Field, typ.Type, typ.Value),
		})
	case strings.HasPrefix(err.Error(), "json: unknown field"):
		if ds := keyDiagnostics(data); len(ds) > 0 {
			return newDecodeError(ds...)
		}
	}
	return newDecodeError(model.Diagnostic{Severity: model.SevError, Code: CodeSchema, Message: err.Error()})
}

var (
	typeCond   = reflect.TypeOf(model.Cond{})
	typeInject = reflect.TypeOf(model.Inject{})
	typeSList  = reflect.TypeOf(model.StringList{})
	typeRaw    = reflect.TypeOf(json.RawMessage{})
)

// keyDiagnostics reports every object key that the schema would reject but
// encoding/json accepts or only partly explains: unknown keys (matched
// case-sensitively, with a JSON pointer; unknown keys of a Cond are
// unknown-predicate) and duplicate keys within one object.
func keyDiagnostics(data []byte) []model.Diagnostic {
	var out []model.Diagnostic
	var doc any
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil
	}
	walkUnknown(doc, reflect.TypeOf(model.RuleFile{}), "", &out)
	dec := json.NewDecoder(bytes.NewReader(data))
	walkDuplicates(dec, reflect.TypeOf(model.RuleFile{}), "", &out)
	return out
}

// walkDuplicates consumes one JSON value from dec, walking it alongside t
// (nil or an opaque type skips the value) and reporting duplicate keys.
func walkDuplicates(dec *json.Decoder, t reflect.Type, at string, out *[]model.Diagnostic) bool {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == typeSList || t == typeRaw {
		t = nil
	}
	tok, err := dec.Token()
	if err != nil {
		return false
	}
	d, ok := tok.(json.Delim)
	if !ok {
		return true
	}
	if t == nil {
		for depth := 1; depth > 0; {
			tok, err := dec.Token()
			if err != nil {
				return false
			}
			if d, ok := tok.(json.Delim); ok {
				if d == '{' || d == '[' {
					depth++
				} else {
					depth--
				}
			}
		}
		return true
	}
	switch d {
	case '{':
		var fields map[string]reflect.Type
		if t.Kind() == reflect.Struct && t != typeInject {
			fields = jsonFields(t)
		}
		seen := map[string]bool{}
		for dec.More() {
			kt, err := dec.Token()
			if err != nil {
				return false
			}
			k, _ := kt.(string)
			if seen[k] {
				*out = append(*out, model.Diagnostic{
					Severity: model.SevError, Code: CodeSchema, Pointer: at + ptr(k),
					Message: fmt.Sprintf("duplicate key %q", k),
				})
			}
			seen[k] = true
			var ct reflect.Type
			switch {
			case fields != nil:
				ct = fields[k]
			case t.Kind() == reflect.Map:
				ct = t.Elem()
			}
			if !walkDuplicates(dec, ct, at+ptr(k), out) {
				return false
			}
		}
	case '[':
		var et reflect.Type
		if t.Kind() == reflect.Slice {
			et = t.Elem()
		}
		for i := 0; dec.More(); i++ {
			if !walkDuplicates(dec, et, at+ptr(i), out) {
				return false
			}
		}
	}
	_, err = dec.Token()
	return err == nil
}

func jsonFields(t reflect.Type) map[string]reflect.Type {
	m := map[string]reflect.Type{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		tag := f.Tag.Get("json")
		name := strings.Split(tag, ",")[0]
		if f.Anonymous && name == "" {
			ft := f.Type
			if ft.Kind() == reflect.Struct {
				for k, v := range jsonFields(ft) {
					m[k] = v
				}
			}
			continue
		}
		if name == "-" || !f.IsExported() {
			continue
		}
		if name == "" {
			name = f.Name
		}
		m[name] = f.Type
	}
	return m
}

func walkUnknown(v any, t reflect.Type, at string, out *[]model.Diagnostic) {
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	switch {
	case t == typeSList || t == typeRaw:
		return
	case t == typeInject:
		m, ok := v.(map[string]any)
		if !ok {
			return
		}
		for _, k := range sortedKeys(m) {
			if k != "text" && k != "anchor" {
				*out = append(*out, unknownKeyDiag(CodeUnknownField, at, k))
			}
		}
		return
	}
	switch t.Kind() {
	case reflect.Struct:
		m, ok := v.(map[string]any)
		if !ok {
			return
		}
		fields := jsonFields(t)
		code := CodeUnknownField
		if t == typeCond {
			code = CodeUnknownPredicate
		}
		keys := make([]string, 0, len(m))
		for k := range m {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			ft, ok := fields[k]
			if !ok {
				*out = append(*out, unknownKeyDiag(code, at, k))
				continue
			}
			walkUnknown(m[k], ft, at+ptr(k), out)
		}
	case reflect.Slice:
		arr, ok := v.([]any)
		if !ok {
			return
		}
		for i, el := range arr {
			walkUnknown(el, t.Elem(), at+ptr(i), out)
		}
	case reflect.Map:
		m, ok := v.(map[string]any)
		if !ok {
			return
		}
		for _, k := range sortedKeys(m) {
			walkUnknown(m[k], t.Elem(), at+ptr(k), out)
		}
	}
}

func unknownKeyDiag(code, parent, key string) model.Diagnostic {
	what := "unknown field"
	if code == CodeUnknownPredicate {
		what = "unknown predicate or combinator"
	}
	return model.Diagnostic{
		Severity: model.SevError, Code: code, Pointer: parent + ptr(key),
		Message: fmt.Sprintf("%s %q", what, key),
	}
}
