package main

// Data: JSON, YAML, TOML, XML, CSV/TSV and Excel, through Go values. Tables (CSV, TSV, XLSX) are
// rows; as records they are a list of objects keyed by the first row, and back.

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/xuri/excelize/v2"
	"gopkg.in/yaml.v3"
)

func init() {
	families = append(families, &family{
		name: "data",
		in:   exts("json yaml yml toml xml csv tsv xlsx xlsm"),
		out: []target{
			{ID: "json", Label: "JSON", Icon: "code"},
			{ID: "yaml", Label: "YAML", Icon: "code"},
			{ID: "csv", Label: "CSV", Icon: "grid"},
			{ID: "xlsx", Label: "Excel", Hint: "XLSX", Icon: "grid"},
			{ID: "toml", Label: "TOML", Back: true, Icon: "code"},
			{ID: "xml", Label: "XML", Back: true, Icon: "code"},
			{ID: "tsv", Label: "TSV", Back: true, Icon: "grid"},
			{ID: "md", Label: "Table", Hint: "Markdown table", Back: true, Icon: "lines"},
		},
		convert: convertData,
	})
}

func readData(p string) (any, error) {
	ext := convExt(p)
	if ext == ".xlsx" || ext == ".xlsm" {
		f, err := excelize.OpenFile(p)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		rows, err := f.GetRows(f.GetSheetName(f.GetActiveSheetIndex()))
		if err != nil {
			return nil, err
		}
		return rows, nil
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	b = bytes.TrimPrefix(b, []byte{0xEF, 0xBB, 0xBF})
	var v any
	switch ext {
	case ".json":
		d := json.NewDecoder(bytes.NewReader(b))
		d.UseNumber()
		err = d.Decode(&v)
	case ".yaml", ".yml":
		err = yaml.Unmarshal(b, &v)
	case ".toml":
		var m map[string]any
		err = toml.Unmarshal(b, &m)
		v = m
	case ".xml":
		v, err = xmlValue(b)
	case ".csv", ".tsv":
		r := csv.NewReader(bytes.NewReader(b))
		r.FieldsPerRecord = -1
		r.LazyQuotes = true
		if ext == ".tsv" {
			r.Comma = '\t'
		} else if first, _, _ := bytes.Cut(b, []byte("\n")); bytes.Count(first, []byte(";")) > bytes.Count(first, []byte(",")) {
			r.Comma = ';' // Excel in many countries
		}
		var rows [][]string
		rows, err = r.ReadAll()
		v = rows
	default:
		return nil, errors.New("unknown data format")
	}
	if err != nil {
		return nil, fmt.Errorf("can't read it (%v)", err)
	}
	return v, nil
}

// asRows: a value as a table (records -> header + rows, an object -> key/value rows).
func asRows(v any) [][]string {
	switch v := v.(type) {
	case [][]string:
		return v
	case []any:
		var keys []string
		seen := map[string]bool{}
		for _, r := range v {
			if m, ok := r.(map[string]any); ok {
				for _, k := range sortedKeys(m) {
					if !seen[k] {
						seen[k] = true
						keys = append(keys, k)
					}
				}
			}
		}
		if len(keys) == 0 {
			out := [][]string{{"value"}}
			for _, r := range v {
				out = append(out, []string{cell(r)})
			}
			return out
		}
		out := [][]string{keys}
		for _, r := range v {
			m, _ := r.(map[string]any)
			row := make([]string, len(keys))
			for i, k := range keys {
				row[i] = cell(m[k])
			}
			out = append(out, row)
		}
		return out
	case map[string]any:
		// one list inside (an API's {"items": [...]}): that list
		var only []any
		for _, x := range v {
			if l, ok := x.([]any); ok {
				if only != nil {
					only = nil
					break
				}
				only = l
			}
		}
		if only != nil && len(v) == 1 {
			return asRows(only)
		}
		out := [][]string{{"key", "value"}}
		for _, k := range sortedKeys(v) {
			out = append(out, []string{k, cell(v[k])})
		}
		return out
	}
	return [][]string{{"value"}, {cell(v)}}
}

// asRecords: a table as a list of objects keyed by its first row (numbers stay numbers).
func asRecords(v any) any {
	rows, ok := v.([][]string)
	if !ok {
		return v
	}
	if len(rows) == 0 {
		return []any{}
	}
	head := rows[0]
	out := make([]any, 0, len(rows)-1)
	for _, r := range rows[1:] {
		m := map[string]any{}
		for i, h := range head {
			if h == "" {
				h = "column" + strconv.Itoa(i+1)
			}
			s := ""
			if i < len(r) {
				s = r[i]
			}
			m[h] = scalar(s)
		}
		out = append(out, m)
	}
	return out
}

func scalar(s string) any {
	if n, err := strconv.ParseInt(s, 10, 64); err == nil && (s == "0" || !strings.HasPrefix(s, "0")) {
		return n
	}
	if f, err := strconv.ParseFloat(s, 64); err == nil && !strings.HasPrefix(s, "0") && strings.ContainsAny(s, ".eE") {
		return f
	}
	switch s {
	case "true":
		return true
	case "false":
		return false
	}
	return s
}

func cell(v any) string {
	switch v := v.(type) {
	case nil:
		return ""
	case string:
		return v
	case map[string]any, []any:
		b, _ := json.Marshal(v)
		return string(b)
	}
	return fmt.Sprint(v)
}

func sortedKeys(m map[string]any) []string {
	k := make([]string, 0, len(m))
	for x := range m {
		k = append(k, x)
	}
	sort.Strings(k)
	return k
}

// plain: YAML/TOML/JSON-number values as JSON-able ones.
func plain(v any) any {
	switch v := v.(type) {
	case map[string]any:
		for k, x := range v {
			v[k] = plain(x)
		}
		return v
	case map[any]any:
		m := map[string]any{}
		for k, x := range v {
			m[fmt.Sprint(k)] = plain(x)
		}
		return m
	case []map[string]any:
		out := make([]any, len(v))
		for i, x := range v {
			out[i] = plain(x)
		}
		return out
	case []any:
		for i, x := range v {
			v[i] = plain(x)
		}
		return v
	case json.Number:
		if n, err := v.Int64(); err == nil {
			return n
		}
		f, _ := v.Float64()
		return f
	}
	return v
}

func convertData(ctx context.Context, src string, t target, dir string) ([]string, error) {
	v, err := readData(src)
	if err != nil {
		return nil, err
	}
	v = plain(v)
	ext := "." + t.ID
	if t.ID == "md" {
		ext = ".md"
	}
	p := outPath(dir, stem(src), ext)
	return []string{p}, writeOut(p, func(f *os.File) error { return writeData(f, v, t.ID) })
}

func writeData(w io.Writer, v any, format string) error {
	switch format {
	case "json":
		e := json.NewEncoder(w)
		e.SetIndent("", "  ")
		e.SetEscapeHTML(false)
		return e.Encode(asRecords(v))
	case "yaml":
		e := yaml.NewEncoder(w)
		e.SetIndent(2)
		if err := e.Encode(asRecords(v)); err != nil {
			return err
		}
		return e.Close()
	case "toml":
		r := asRecords(v)
		if _, ok := r.(map[string]any); !ok {
			r = map[string]any{"items": r} // TOML's top level is a table
		}
		return toml.NewEncoder(w).Encode(r)
	case "xml":
		if _, err := io.WriteString(w, xml.Header); err != nil {
			return err
		}
		e := xml.NewEncoder(w)
		e.Indent("", "  ")
		if err := xmlWrite(e, "root", asRecords(v)); err != nil {
			return err
		}
		if err := e.Flush(); err != nil {
			return err
		}
		_, err := io.WriteString(w, "\n")
		return err
	case "csv", "tsv":
		if format == "csv" {
			w.Write([]byte{0xEF, 0xBB, 0xBF}) // Excel reads UTF-8 with the mark
		}
		cw := csv.NewWriter(w)
		if format == "tsv" {
			cw.Comma = '\t'
		}
		cw.WriteAll(asRows(v))
		return cw.Error()
	case "xlsx":
		f := excelize.NewFile()
		defer f.Close()
		sh := f.GetSheetName(0)
		for i, r := range asRows(v) {
			row := make([]any, len(r))
			for j, c := range r {
				if i > 0 {
					row[j] = scalar(c)
				} else {
					row[j] = c
				}
			}
			at, _ := excelize.CoordinatesToCellName(1, i+1)
			if err := f.SetSheetRow(sh, at, &row); err != nil {
				return err
			}
		}
		_, err := f.WriteTo(w)
		return err
	case "md":
		rows := asRows(v)
		for i := range rows {
			for j := range rows[i] {
				rows[i][j] = strings.ReplaceAll(strings.ReplaceAll(rows[i][j], "|", `\|`), "\n", " ")
			}
		}
		_, err := io.WriteString(w, mdTable(rows))
		return err
	}
	return fmt.Errorf("can't write %s", format)
}

// ---------------------------------------------------------------- XML <-> values

// xmlValue: elements become objects, repeated ones lists, attributes "@name", text "#text"
// (or the value itself when an element has only text).
func xmlValue(b []byte) (any, error) {
	d := xml.NewDecoder(bytes.NewReader(b))
	type node struct {
		name string
		m    map[string]any
		text strings.Builder
	}
	var stack []*node
	var root map[string]any
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			n := &node{name: t.Name.Local, m: map[string]any{}}
			for _, a := range t.Attr {
				n.m["@"+a.Name.Local] = a.Value
			}
			stack = append(stack, n)
		case xml.CharData:
			if len(stack) > 0 {
				stack[len(stack)-1].text.Write(t)
			}
		case xml.EndElement:
			n := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			var val any = n.m
			txt := strings.TrimSpace(n.text.String())
			if len(n.m) == 0 {
				val = scalar(txt)
			} else if txt != "" {
				n.m["#text"] = txt
			}
			if len(stack) == 0 {
				root = map[string]any{n.name: val}
				continue
			}
			pm := stack[len(stack)-1].m
			switch old := pm[n.name].(type) {
			case nil:
				pm[n.name] = val
			case []any:
				pm[n.name] = append(old, val)
			default:
				pm[n.name] = []any{old, val}
			}
		}
	}
	if root == nil {
		return nil, errors.New("no XML element")
	}
	return root, nil
}

func xmlName(s string) string {
	var b strings.Builder
	for i, r := range s {
		switch {
		case r == '_' || r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r > 127:
			b.WriteRune(r)
		case (r >= '0' && r <= '9' || r == '-' || r == '.') && i > 0:
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	if b.Len() == 0 {
		return "item"
	}
	return b.String()
}

func xmlWrite(e *xml.Encoder, name string, v any) error {
	name = xmlName(name)
	switch v := v.(type) {
	case map[string]any:
		if len(v) == 1 && name == "root" { // {"catalog": {...}}: that's the root
			for k, x := range v {
				if _, ok := x.([]any); !ok {
					return xmlWrite(e, k, x)
				}
			}
		}
		start := xml.StartElement{Name: xml.Name{Local: name}}
		for _, k := range sortedKeys(v) {
			if strings.HasPrefix(k, "@") {
				start.Attr = append(start.Attr, xml.Attr{Name: xml.Name{Local: xmlName(k[1:])}, Value: cell(v[k])})
			}
		}
		if err := e.EncodeToken(start); err != nil {
			return err
		}
		for _, k := range sortedKeys(v) {
			switch {
			case strings.HasPrefix(k, "@"):
			case k == "#text":
				if err := e.EncodeToken(xml.CharData(cell(v[k]))); err != nil {
					return err
				}
			default:
				if err := xmlWrite(e, k, v[k]); err != nil {
					return err
				}
			}
		}
		return e.EncodeToken(start.End())
	case []any:
		if name == "root" {
			start := xml.StartElement{Name: xml.Name{Local: "root"}}
			e.EncodeToken(start)
			for _, x := range v {
				if err := xmlWrite(e, "item", x); err != nil {
					return err
				}
			}
			return e.EncodeToken(start.End())
		}
		for _, x := range v {
			if err := xmlWrite(e, name, x); err != nil {
				return err
			}
		}
		return nil
	}
	return e.EncodeElement(cell(v), xml.StartElement{Name: xml.Name{Local: name}})
}
