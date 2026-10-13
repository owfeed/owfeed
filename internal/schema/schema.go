// Package schema generates the JSON Schema for owfeed.yml from the config package.
//
// It reads internal/config with go/ast rather than reflecting over the structs,
// because reflection sees types and yaml tags and nothing else — and the part of
// config.go worth publishing is the prose. "It is never \"all\": apk rejects
// \"all\" as uninstallable" is the sentence that stops someone writing a package
// nobody can install, and it exists only as a doc comment.
//
// WHAT THE SCHEMA DOES NOT SAY. It describes the shape of a config, not the rules
// applied to one. Those live in validate.go as code — `build: sdk` parses and is
// then refused by design, `overrides` is a known key that this version rejects,
// release lines must not collide, `conffiles` must be relative. None of that is
// expressible here, and pretending otherwise would produce a schema that shows a
// config green in an editor which owfeed then refuses. The validator remains the
// specification; this is autocompletion and a typo check.
package schema

import (
	"bytes"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ID is where the schema is published. `owfeed init` writes it into the modeline
// of every config it scaffolds, so it must be a URL that answers.
const ID = "https://owfeed.org/schema/v1.json"

// Root is the struct the document describes.
const Root = "Config"

// customTypes are the types whose YAML shape is decided by an UnmarshalYAML
// method rather than by their fields. An AST cannot infer what those accept, so
// each is written out here, next to a note naming the method that has to agree.
var customTypes = map[string]map[string]any{
	// config.Arches.UnmarshalYAML: the scalar "auto", or a sequence.
	"Arches": {
		"oneOf": []any{
			map[string]any{"const": "auto"},
			map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
	},
	// config.PkgArch.UnmarshalYAML: one architecture, or a list of them.
	"PkgArch": {
		"oneOf": []any{
			map[string]any{"type": "string"},
			map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
		},
	},
}

// enums ties a field to the constant group that defines its accepted values, so
// the values themselves come from the code. The association is by hand; the
// values are not, which is the half that drifts.
var enums = map[string][]string{
	"Release.format":      {"FormatAPK", "FormatIPK"},
	"Package.build":       {"BuildMkpkg", "BuildSDK"},
	"Package.compression": {"CompressionZopfli"},
	"Publish.target":      {"TargetGitHubPages", "TargetS3", "TargetRsync"},
}

// constFields are fields with exactly one accepted value, named by the constant
// that holds it.
var constFields = map[string]string{
	"Config.version": "SchemaVersion",
}

// refused are keys the validator rejects even though they parse. Marking them
// deprecated is the closest this format gets to saying so: an editor strikes them
// through instead of showing a config green that owfeed will refuse to run.
//
// A key leaves this list when it is implemented, or when it is deleted from the
// struct -- and if it is deleted, the generator fails here rather than silently
// dropping the warning.
var refused = map[string]string{
	"Config.overrides":        "not implemented: remove the block",
	"Config.retention":        "not implemented: nothing is garbage-collected in this version",
	"Build.changed-only":      "not implemented: every configured package is built",
	"Signing.keyring-package": "not implemented: install the key as the generated snippet describes",
}

// Generate produces the schema document for the config package at dir.
func Generate(dir string) ([]byte, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		// Tests describe the format's edge cases, not the format.
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		names = append(names, name)
	}
	// Deterministic, because the output is a checked-in file compared byte for byte.
	sort.Strings(names)

	fset := token.NewFileSet()
	structs := map[string]*ast.StructType{}
	docs := map[string]string{}
	consts := map[string]string{}
	nums := map[string]json.Number{}
	for _, name := range names {
		file, err := parser.ParseFile(fset, filepath.Join(dir, name), nil, parser.ParseComments)
		if err != nil {
			return nil, err
		}
		collect(file, structs, docs, consts, nums)
	}
	if _, ok := structs[Root]; !ok {
		return nil, fmt.Errorf("no %s struct in %s", Root, dir)
	}

	// Only what the root actually reaches. The config package holds types that are
	// not part of the file format at all -- Error is a diagnostic -- and publishing
	// them as definitions would describe fields nobody can write.
	reachable := map[string]bool{}
	reach(Root, structs, reachable)

	seenAnnotation = map[string]bool{}
	defs := map[string]any{}
	for name := range reachable {
		if _, custom := customTypes[name]; custom {
			continue
		}
		obj, err := object(name, structs, docs, consts, nums)
		if err != nil {
			return nil, err
		}
		defs[name] = obj
	}
	for _, table := range []map[string]bool{keys(enums), keys(constFields), keys(refused)} {
		for k := range table {
			if !seenAnnotation[k] {
				return nil, fmt.Errorf("%s is annotated in internal/schema but no such field exists in internal/config", k)
			}
		}
	}

	root := defs[Root].(map[string]any)
	delete(defs, Root)

	doc := map[string]any{
		"$schema":     "https://json-schema.org/draft/2020-12/schema",
		"$id":         ID,
		"title":       "owfeed.yml",
		"description": docs[Root],
		"$comment": "Generated from internal/config by internal/schema. This describes the shape of a " +
			"config, not the rules owfeed applies to one: some keys parse here and are then refused by " +
			"the validator, which remains the specification. Regenerate with `go test ./internal/schema -update`.",
		"$defs": defs,
	}
	for k, v := range root {
		doc[k] = v
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetIndent("", "  ")
	// The descriptions are prose lifted out of config.go, and they are full of
	// <name>.pem and >= comparisons. Escaped to < they are unreadable in the
	// tooltip that is the whole point of publishing them.
	enc.SetEscapeHTML(false)
	if err := enc.Encode(doc); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// reach walks the type graph from a root struct, recording every struct the file
// format can actually contain.
func reach(name string, structs map[string]*ast.StructType, seen map[string]bool) {
	if seen[name] {
		return
	}
	st, ok := structs[name]
	if !ok {
		return
	}
	seen[name] = true
	for _, field := range st.Fields.List {
		if yamlKey(field) == "" {
			continue
		}
		for _, ref := range named(field.Type) {
			reach(ref, structs, seen)
		}
	}
}

// named lists the type names a field's type mentions.
func named(expr ast.Expr) []string {
	switch t := expr.(type) {
	case *ast.Ident:
		return []string{t.Name}
	case *ast.StarExpr:
		return named(t.X)
	case *ast.ArrayType:
		return named(t.Elt)
	case *ast.MapType:
		return append(named(t.Key), named(t.Value)...)
	}
	return nil
}

// collect gathers struct types, their doc comments and every literal constant.
//
// Numbers are kept apart from strings so that a `const: 1` in the schema is the
// number 1 and not the string "1" -- a config saying `version: "1"` is rejected
// by the YAML decode, and a schema that accepted it would disagree with the tool.
func collect(file *ast.File, structs map[string]*ast.StructType, docs, consts map[string]string, nums map[string]json.Number) {
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gen.Specs {
			switch s := spec.(type) {
			case *ast.TypeSpec:
				st, ok := s.Type.(*ast.StructType)
				if !ok {
					continue
				}
				structs[s.Name.Name] = st
				// A single-spec declaration carries its comment on the GenDecl.
				doc := s.Doc
				if doc == nil {
					doc = gen.Doc
				}
				docs[s.Name.Name] = prose(doc)
			case *ast.ValueSpec:
				for i, n := range s.Names {
					if i >= len(s.Values) {
						continue
					}
					lit, ok := s.Values[i].(*ast.BasicLit)
					if !ok {
						continue
					}
					switch lit.Kind {
					case token.STRING:
						if v, err := strconv.Unquote(lit.Value); err == nil {
							consts[n.Name] = v
						}
					case token.INT, token.FLOAT:
						nums[n.Name] = json.Number(lit.Value)
					}
				}
			}
		}
	}
}

// seenAnnotation records which enums/constFields/refused entries were applied, so
// Generate can fail on one that matched nothing. A renamed yaml key would
// otherwise take its enum or its deprecation with it, silently.
var seenAnnotation = map[string]bool{}

// object turns one struct into a JSON Schema object.
func object(name string, structs map[string]*ast.StructType, docs, consts map[string]string, nums map[string]json.Number) (map[string]any, error) {
	props := map[string]any{}
	for _, field := range structs[name].Fields.List {
		key := yamlKey(field)
		if key == "" {
			continue
		}
		prop, err := schemaFor(field.Type, structs)
		if err != nil {
			return nil, fmt.Errorf("%s.%s: %w", name, key, err)
		}
		if d := prose(field.Doc); d != "" {
			prop["description"] = d
		}
		qualified := name + "." + key
		if group, ok := enums[qualified]; ok {
			var values []any
			for _, c := range group {
				v, ok := consts[c]
				if !ok {
					return nil, fmt.Errorf("%s: no constant %s", qualified, c)
				}
				values = append(values, v)
			}
			prop["enum"] = values
			seenAnnotation[qualified] = true
		}
		if c, ok := constFields[qualified]; ok {
			switch {
			case nums[c] != "":
				prop["const"] = nums[c]
			case consts[c] != "":
				prop["const"] = consts[c]
			default:
				return nil, fmt.Errorf("%s: no constant %s", qualified, c)
			}
			seenAnnotation[qualified] = true
		}
		if why, ok := refused[qualified]; ok {
			prop["deprecated"] = true
			if d, _ := prop["description"].(string); d != "" {
				prop["description"] = d + " -- " + why
			} else {
				prop["description"] = why
			}
			seenAnnotation[qualified] = true
		}
		props[key] = prop
	}

	keys := make([]string, 0, len(props))
	for k := range props {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	return map[string]any{
		"type":       "object",
		"properties": props,
		// Unknown keys are an error in owfeed, not a warning — the one rule from
		// validate.go that this format can state, so it states it.
		"additionalProperties": false,
	}, nil
}

// schemaFor maps a Go type to its JSON Schema equivalent.
func schemaFor(expr ast.Expr, structs map[string]*ast.StructType) (map[string]any, error) {
	switch t := expr.(type) {
	case *ast.Ident:
		switch t.Name {
		case "string":
			return map[string]any{"type": "string"}, nil
		case "int", "int64":
			return map[string]any{"type": "integer"}, nil
		case "bool":
			return map[string]any{"type": "boolean"}, nil
		case "any":
			return map[string]any{}, nil
		}
		if custom, ok := customTypes[t.Name]; ok {
			return clone(custom), nil
		}
		if _, ok := structs[t.Name]; ok {
			return map[string]any{"$ref": "#/$defs/" + t.Name}, nil
		}
		// A named string type, such as BuildMode.
		return map[string]any{"type": "string"}, nil
	case *ast.StarExpr:
		return schemaFor(t.X, structs)
	case *ast.ArrayType:
		items, err := schemaFor(t.Elt, structs)
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "array", "items": items}, nil
	case *ast.MapType:
		values, err := schemaFor(t.Value, structs)
		if err != nil {
			return nil, err
		}
		return map[string]any{"type": "object", "additionalProperties": values}, nil
	case *ast.InterfaceType:
		return map[string]any{}, nil
	}
	return nil, fmt.Errorf("unsupported type %T", expr)
}

// yamlKey reads a field's yaml tag. An unexported field, or one without a tag,
// is not part of the format.
func yamlKey(field *ast.Field) string {
	if field.Tag == nil || len(field.Names) == 0 || !field.Names[0].IsExported() {
		return ""
	}
	tag, err := strconv.Unquote(field.Tag.Value)
	if err != nil {
		return ""
	}
	const key = `yaml:"`
	i := strings.Index(tag, key)
	if i < 0 {
		return ""
	}
	rest := tag[i+len(key):]
	j := strings.Index(rest, `"`)
	if j < 0 {
		return ""
	}
	return strings.Split(rest[:j], ",")[0]
}

// prose flattens a doc comment into one line.
//
// Paragraph breaks become sentence breaks: an editor renders a description in a
// tooltip, where a hard-wrapped comment reads as one run-on line anyway.
func prose(doc *ast.CommentGroup) string {
	if doc == nil {
		return ""
	}
	var paras []string
	var cur []string
	for _, c := range doc.List {
		line := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(c.Text, "//"), " "))
		if line == "" {
			if len(cur) > 0 {
				paras = append(paras, strings.Join(cur, " "))
				cur = nil
			}
			continue
		}
		cur = append(cur, line)
	}
	if len(cur) > 0 {
		paras = append(paras, strings.Join(cur, " "))
	}
	return strings.Join(paras, " ")
}

// keys is the key set of any of the annotation tables.
func keys[V any](m map[string]V) map[string]bool {
	out := make(map[string]bool, len(m))
	for k := range m {
		out[k] = true
	}
	return out
}

func clone(m map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range m {
		out[k] = v
	}
	return out
}
