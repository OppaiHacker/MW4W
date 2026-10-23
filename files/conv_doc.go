package main

// Documents: everything goes through Markdown. In: Markdown, text, HTML, DOCX, ODT, EPUB and the text
// of PDFs. Out: PDF (laid out here, Segoe UI), DOCX, HTML, EPUB, Markdown, text.

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"html"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/go-pdf/fpdf"
	pdftext "github.com/ledongthuc/pdf"
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	gmhtml "github.com/yuin/goldmark/renderer/html"
	"github.com/yuin/goldmark/text"
)

var md = goldmark.New(goldmark.WithExtensions(extension.GFM), goldmark.WithRendererOptions(gmhtml.WithXHTML()))

func init() {
	families = append(families, &family{
		name: "doc",
		in:   exts("md markdown txt text html htm xhtml docx odt epub"),
		out: []target{
			{ID: "pdf", Label: "PDF", Icon: "pdf"},
			{ID: "docx", Label: "DOCX", Hint: "Word", Icon: "doc"},
			{ID: "md", Label: "Markdown", Icon: "lines"},
			{ID: "html", Label: "HTML", Icon: "code"},
			{ID: "txt", Label: "TXT", Hint: "plain text", Icon: "lines"},
			{ID: "epub", Label: "EPUB", Hint: "e-book", Back: true, Icon: "doc"},
		},
		convert: convertDoc,
	})
}

// toMarkdown reads any document as Markdown (title: its name, or what it says it is).
func toMarkdown(p string) (src, title string, err error) {
	title = stem(p)
	switch convExt(p) {
	case ".md", ".markdown":
		b, err := os.ReadFile(p)
		return string(b), title, err
	case ".txt", ".text":
		b, err := os.ReadFile(p)
		if err != nil {
			return "", "", err
		}
		return textToMarkdown(decodeText(b)), title, nil
	case ".html", ".htm", ".xhtml":
		b, err := os.ReadFile(p)
		if err != nil {
			return "", "", err
		}
		if m := regexp.MustCompile(`(?is)<title[^>]*>(.*?)</title>`).FindSubmatch(b); m != nil && len(bytes.TrimSpace(m[1])) > 0 {
			title = html.UnescapeString(strings.TrimSpace(string(m[1])))
		}
		s, err := htmltomarkdown.ConvertString(decodeText(b))
		return s, title, err
	case ".docx":
		s, err := docxMarkdown(p)
		return s, title, err
	case ".odt":
		s, err := odtMarkdown(p)
		return s, title, err
	case ".epub":
		return epubMarkdown(p)
	case ".pdf":
		return toMarkdownPDF(p)
	}
	return "", "", errors.New("unknown document format")
}

// toMarkdownPDF: a PDF's text.
func toMarkdownPDF(p string) (string, string, error) {
	f, r, err := pdftext.Open(p)
	if err != nil {
		return "", "", err
	}
	defer f.Close()
	tr, err := r.GetPlainText()
	if err != nil {
		return "", "", err
	}
	b, err := io.ReadAll(tr)
	if err != nil {
		return "", "", err
	}
	if len(bytes.TrimSpace(b)) == 0 {
		return "", "", errors.New("the PDF has no text (a scan?)")
	}
	return textToMarkdown(string(b)), stem(p), nil
}

// decodeText: UTF-8, or UTF-16 with a BOM, or Windows-1250/1252 bytes taken as Latin-1.
func decodeText(b []byte) string {
	switch {
	case bytes.HasPrefix(b, []byte{0xEF, 0xBB, 0xBF}):
		return string(b[3:])
	case len(b) >= 2 && (b[0] == 0xFF && b[1] == 0xFE || b[0] == 0xFE && b[1] == 0xFF):
		le := b[0] == 0xFF
		var r []rune
		for i := 2; i+1 < len(b); i += 2 {
			u := uint16(b[i])<<8 | uint16(b[i+1])
			if le {
				u = uint16(b[i+1])<<8 | uint16(b[i])
			}
			r = append(r, rune(u))
		}
		return string(r)
	case utf8.Valid(b):
		return string(b)
	}
	r := make([]rune, len(b))
	for i, c := range b {
		r[i] = rune(c)
	}
	return string(r)
}

// textToMarkdown keeps plain text as it is (Markdown would join lines and read symbols).
func textToMarkdown(s string) string {
	var b strings.Builder
	for _, l := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		l = strings.TrimRight(l, " \t")
		for _, c := range `\*_#>+-[]()!|` + "`" {
			l = strings.ReplaceAll(l, string(c), `\`+string(c))
		}
		if l == "" {
			b.WriteString("\n")
		} else {
			b.WriteString(l + "  \n")
		}
	}
	return b.String()
}

func convertDoc(ctx context.Context, src string, t target, dir string) ([]string, error) {
	m, title, err := toMarkdown(src)
	if err != nil {
		return nil, err
	}
	doc := md.Parser().Parse(text.NewReader([]byte(m)))
	p := outPath(dir, stem(src), "."+t.ID)
	return []string{p}, writeOut(p, func(f *os.File) error {
		switch t.ID {
		case "md":
			_, err := io.WriteString(f, m)
			return err
		case "txt":
			_, err := io.WriteString(f, plainText(doc, []byte(m)))
			return err
		case "html":
			body, err := mdHTML(m)
			if err != nil {
				return err
			}
			_, err = io.WriteString(f, htmlPage(title, body))
			return err
		case "pdf":
			return mdPDF(f, doc, []byte(m), title)
		case "docx":
			return mdDOCX(f, doc, []byte(m))
		case "epub":
			body, err := mdHTML(m)
			if err != nil {
				return err
			}
			return writeEPUB(f, title, body)
		}
		return fmt.Errorf("can't write %s", t.ID)
	})
}

func mdHTML(m string) (string, error) {
	var b bytes.Buffer
	err := md.Convert([]byte(m), &b)
	return b.String(), err
}

func htmlPage(title, body string) string {
	return `<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1"><title>` + html.EscapeString(title) + `</title>
<style>
body{max-width:760px;margin:40px auto;padding:0 20px;font:16px/1.6 "Segoe UI",system-ui,sans-serif;color:#1f2328;background:#fff}
@media (prefers-color-scheme:dark){body{color:#e6edf3;background:#0d1117}code,pre{background:#161b22!important}th,td{border-color:#30363d!important}}
h1,h2,h3{line-height:1.25}code{font:0.9em Consolas,monospace;background:#f3f4f6;padding:.15em .35em;border-radius:4px}
pre{background:#f3f4f6;padding:12px 14px;border-radius:8px;overflow:auto}pre code{background:none;padding:0}
table{border-collapse:collapse}th,td{border:1px solid #d0d7de;padding:6px 12px}blockquote{margin:0;padding-left:14px;border-left:4px solid #d0d7de;color:#656d76}
img{max-width:100%}
</style></head><body>
` + body + `</body></html>
`
}

// ---------------------------------------------------------------- Markdown -> text

func plainText(doc ast.Node, src []byte) string {
	var b strings.Builder
	textBlocks(&b, doc, src, "")
	return strings.TrimRight(b.String(), "\n") + "\n"
}

func textBlocks(b *strings.Builder, n ast.Node, src []byte, prefix string) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch c := c.(type) {
		case *ast.Heading, *ast.Paragraph, *ast.TextBlock:
			b.WriteString(prefix + inlineText(c, src) + "\n")
			if _, ok := c.(*ast.TextBlock); !ok {
				b.WriteString("\n")
			}
		case *ast.List:
			i := c.Start
			for it := c.FirstChild(); it != nil; it = it.NextSibling() {
				mark := "• "
				if c.IsOrdered() {
					mark = strconv.Itoa(i) + ". "
					i++
				}
				var ib strings.Builder
				textBlocks(&ib, it, src, "")
				for k, l := range strings.Split(strings.TrimRight(ib.String(), "\n"), "\n") {
					if k == 0 {
						b.WriteString(prefix + mark + l + "\n")
					} else {
						b.WriteString(prefix + strings.Repeat(" ", utf8.RuneCountInString(mark)) + l + "\n")
					}
				}
			}
			b.WriteString("\n")
		case *ast.FencedCodeBlock, *ast.CodeBlock:
			for i := 0; i < c.Lines().Len(); i++ {
				s := c.Lines().At(i)
				b.WriteString(prefix + "    " + strings.TrimRight(string(s.Value(src)), "\n") + "\n")
			}
			b.WriteString("\n")
		case *ast.Blockquote:
			textBlocks(b, c, src, prefix+"> ")
		case *ast.ThematicBreak:
			b.WriteString(prefix + "----------\n\n")
		case *east.Table:
			for r := c.FirstChild(); r != nil; r = r.NextSibling() {
				var cells []string
				for cell := r.FirstChild(); cell != nil; cell = cell.NextSibling() {
					cells = append(cells, inlineText(cell, src))
				}
				b.WriteString(prefix + strings.Join(cells, "\t") + "\n")
			}
			b.WriteString("\n")
		default:
			textBlocks(b, c, src, prefix)
		}
	}
}

func inlineText(n ast.Node, src []byte) string {
	var b strings.Builder
	ast.Walk(n, func(c ast.Node, enter bool) (ast.WalkStatus, error) {
		if !enter {
			return ast.WalkContinue, nil
		}
		switch c := c.(type) {
		case *ast.Text:
			b.Write(c.Segment.Value(src))
			if c.HardLineBreak() {
				b.WriteString("\n")
			} else if c.SoftLineBreak() {
				b.WriteString(" ")
			}
		case *ast.String:
			b.Write(c.Value)
		case *ast.CodeSpan:
			for k := c.FirstChild(); k != nil; k = k.NextSibling() {
				if t, ok := k.(*ast.Text); ok {
					b.Write(t.Segment.Value(src))
				}
			}
			return ast.WalkSkipChildren, nil
		case *ast.AutoLink:
			b.Write(c.URL(src))
		}
		return ast.WalkContinue, nil
	})
	return b.String()
}

// ---------------------------------------------------------------- Markdown -> PDF

type pdfDoc struct {
	f        *fpdf.Fpdf
	src      []byte
	size, lh float64
	bold     int
	italic   int
	mono     int
	link     string
	left     float64
}

func fontFile(names ...string) []byte {
	dir := filepath.Join(os.Getenv("SystemRoot"), "Fonts")
	for _, n := range names {
		if b, err := os.ReadFile(filepath.Join(dir, n)); err == nil {
			return b
		}
	}
	return nil
}

func mdPDF(w io.Writer, doc ast.Node, src []byte, title string) error {
	f := fpdf.New("P", "mm", "A4", "")
	f.SetTitle(title, true)
	f.SetCreator("MW4W Files", true)
	reg := fontFile("segoeui.ttf", "arial.ttf")
	if reg == nil {
		return errors.New("no font for PDF text (Segoe UI or Arial)")
	}
	f.AddUTF8FontFromBytes("body", "", reg)
	f.AddUTF8FontFromBytes("body", "B", orBytes(fontFile("segoeuib.ttf", "arialbd.ttf"), reg))
	f.AddUTF8FontFromBytes("body", "I", orBytes(fontFile("segoeuii.ttf", "ariali.ttf"), reg))
	f.AddUTF8FontFromBytes("body", "BI", orBytes(fontFile("segoeuiz.ttf", "arialbi.ttf"), reg))
	f.AddUTF8FontFromBytes("mono", "", orBytes(fontFile("consola.ttf", "cour.ttf"), reg))
	f.SetMargins(20, 20, 20)
	f.SetAutoPageBreak(true, 20)
	f.AddPage()
	d := &pdfDoc{f: f, src: src, size: 11, lh: 5.6, left: 20}
	d.font()
	d.blocks(doc)
	if err := f.Error(); err != nil {
		return err
	}
	return f.Output(w)
}

func orBytes(b, def []byte) []byte {
	if b == nil {
		return def
	}
	return b
}

func (d *pdfDoc) font() {
	if d.mono > 0 {
		d.f.SetFont("mono", "", d.size*0.92)
		return
	}
	st := ""
	if d.bold > 0 {
		st += "B"
	}
	if d.italic > 0 {
		st += "I"
	}
	d.f.SetFont("body", st, d.size)
}

func (d *pdfDoc) setLeft(x float64) {
	d.left = x
	d.f.SetLeftMargin(x)
	d.f.SetX(x)
}

func (d *pdfDoc) blocks(n ast.Node) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		d.block(c)
	}
}

func (d *pdfDoc) block(n ast.Node) {
	f := d.f
	switch n := n.(type) {
	case *ast.Heading:
		sizes := []float64{0, 22, 17, 14, 12.5, 11.5, 11}
		old := d.size
		d.size = sizes[min(n.Level, 6)]
		d.bold++
		f.Ln(d.size * 0.25)
		d.font()
		d.inline(n, d.size*0.5)
		f.Ln(d.size*0.5 + 2)
		d.bold--
		d.size = old
		d.font()
		if n.Level <= 2 {
			f.SetDrawColor(220, 220, 225)
			y := f.GetY() - 1.2
			pw, _ := f.GetPageSize()
			f.Line(d.left, y, pw-20, y)
			f.Ln(1.5)
		}
	case *ast.Paragraph, *ast.TextBlock:
		d.inline(n, d.lh)
		f.Ln(d.lh)
		if _, ok := n.(*ast.Paragraph); ok {
			f.Ln(d.lh * 0.45)
		}
	case *ast.List:
		i := n.Start
		old := d.left
		for it := n.FirstChild(); it != nil; it = it.NextSibling() {
			mark := "•"
			if n.IsOrdered() {
				mark = strconv.Itoa(i) + "."
				i++
			}
			f.SetX(old + 1)
			f.CellFormat(6, d.lh, mark, "", 0, "L", false, 0, "")
			d.setLeft(old + 7)
			d.blocks(it)
			d.setLeft(old)
		}
		f.Ln(d.lh * 0.4)
	case *ast.FencedCodeBlock, *ast.CodeBlock:
		var b strings.Builder
		for i := 0; i < n.Lines().Len(); i++ {
			s := n.Lines().At(i)
			b.Write(s.Value(d.src))
		}
		pw, _ := f.GetPageSize()
		d.mono++
		d.font()
		f.SetFillColor(243, 244, 246)
		f.SetX(d.left)
		f.MultiCell(pw-20-d.left, d.lh*0.95, strings.TrimRight(strings.ReplaceAll(b.String(), "\t", "    "), "\n"), "", "L", true)
		d.mono--
		d.font()
		f.Ln(d.lh * 0.6)
	case *ast.Blockquote:
		old := d.left
		y0 := f.GetY()
		d.setLeft(old + 6)
		f.SetTextColor(100, 105, 115)
		d.blocks(n)
		f.SetTextColor(0, 0, 0)
		f.SetDrawColor(208, 215, 222)
		f.SetLineWidth(1)
		if y1 := f.GetY(); y1 > y0 {
			f.Line(old+1.5, y0, old+1.5, y1-d.lh*0.45)
		}
		f.SetLineWidth(0.2)
		d.setLeft(old)
	case *ast.ThematicBreak:
		pw, _ := f.GetPageSize()
		f.SetDrawColor(200, 200, 205)
		f.Line(d.left, f.GetY()+2, pw-20, f.GetY()+2)
		f.Ln(6)
	case *east.Table:
		pw, _ := f.GetPageSize()
		cols := 0
		for r := n.FirstChild(); r != nil; r = r.NextSibling() {
			k := 0
			for c := r.FirstChild(); c != nil; c = c.NextSibling() {
				k++
			}
			cols = max(cols, k)
		}
		if cols == 0 {
			return
		}
		cw := (pw - 20 - d.left) / float64(cols)
		f.SetDrawColor(208, 215, 222)
		for r := n.FirstChild(); r != nil; r = r.NextSibling() {
			_, head := r.(*east.TableHeader)
			if head {
				d.bold++
			}
			d.font()
			f.SetX(d.left)
			for c := r.FirstChild(); c != nil; c = c.NextSibling() {
				s := inlineText(c, d.src)
				for f.GetStringWidth(s) > cw-2 && len(s) > 1 {
					_, sz := utf8.DecodeLastRuneInString(s)
					s = s[:len(s)-sz]
				}
				f.SetFillColor(246, 248, 250)
				f.CellFormat(cw, d.lh+2, s, "1", 0, "L", head, 0, "")
			}
			f.Ln(-1)
			if head {
				d.bold--
			}
		}
		d.font()
		f.Ln(d.lh * 0.6)
	case *ast.HTMLBlock:
	default:
		d.blocks(n)
	}
}

func (d *pdfDoc) inline(n ast.Node, lh float64) {
	f := d.f
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch c := c.(type) {
		case *ast.Text:
			d.write(lh, string(c.Segment.Value(d.src)))
			if c.HardLineBreak() {
				f.Ln(lh)
			} else if c.SoftLineBreak() {
				d.write(lh, " ")
			}
		case *ast.String:
			d.write(lh, string(c.Value))
		case *ast.Emphasis:
			if c.Level >= 2 {
				d.bold++
			} else {
				d.italic++
			}
			d.font()
			d.inline(c, lh)
			if c.Level >= 2 {
				d.bold--
			} else {
				d.italic--
			}
			d.font()
		case *ast.CodeSpan:
			d.mono++
			d.font()
			d.inline(c, lh)
			d.mono--
			d.font()
		case *ast.Link:
			d.link = string(c.Destination)
			f.SetTextColor(9, 105, 218)
			d.inline(c, lh)
			f.SetTextColor(0, 0, 0)
			d.link = ""
		case *ast.AutoLink:
			u := string(c.URL(d.src))
			f.SetTextColor(9, 105, 218)
			f.WriteLinkString(lh, u, u)
			f.SetTextColor(0, 0, 0)
		case *ast.Image:
			d.write(lh, "["+inlineText(c, d.src)+"]")
		case *east.TaskCheckBox:
			if c.IsChecked {
				d.write(lh, "☑ ")
			} else {
				d.write(lh, "☐ ")
			}
		default:
			d.inline(c, lh)
		}
	}
}

func (d *pdfDoc) write(lh float64, s string) {
	if d.link != "" {
		d.f.WriteLinkString(lh, s, d.link)
		return
	}
	d.f.Write(lh, s)
}

// ---------------------------------------------------------------- Markdown -> DOCX

func mdDOCX(w io.Writer, doc ast.Node, src []byte) error {
	var body strings.Builder
	esc := func(s string) string {
		var b bytes.Buffer
		xml.EscapeText(&b, []byte(s))
		return b.String()
	}
	type style struct{ b, i, mono bool }
	var runs func(n ast.Node, st style)
	runs = func(n ast.Node, st style) {
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			emit := func(s string) {
				body.WriteString("<w:r><w:rPr>")
				if st.b {
					body.WriteString("<w:b/>")
				}
				if st.i {
					body.WriteString("<w:i/>")
				}
				if st.mono {
					body.WriteString(`<w:rFonts w:ascii="Consolas" w:hAnsi="Consolas"/>`)
				}
				body.WriteString(`</w:rPr><w:t xml:space="preserve">` + esc(s) + "</w:t></w:r>")
			}
			switch c := c.(type) {
			case *ast.Text:
				emit(string(c.Segment.Value(src)))
				if c.HardLineBreak() {
					body.WriteString("<w:r><w:br/></w:r>")
				} else if c.SoftLineBreak() {
					emit(" ")
				}
			case *ast.String:
				emit(string(c.Value))
			case *ast.Emphasis:
				s := st
				if c.Level >= 2 {
					s.b = true
				} else {
					s.i = true
				}
				runs(c, s)
			case *ast.CodeSpan:
				s := st
				s.mono = true
				runs(c, s)
			case *ast.AutoLink:
				emit(string(c.URL(src)))
			default:
				runs(c, st)
			}
		}
	}
	para := func(pstyle string, n ast.Node, st style, prefix string) {
		body.WriteString("<w:p>")
		if pstyle != "" {
			body.WriteString(`<w:pPr><w:pStyle w:val="` + pstyle + `"/></w:pPr>`)
		}
		if prefix != "" {
			body.WriteString(`<w:r><w:t xml:space="preserve">` + esc(prefix) + "</w:t></w:r>")
		}
		runs(n, st)
		body.WriteString("</w:p>")
	}
	var list func(l *ast.List, depth int)
	list = func(l *ast.List, depth int) {
		i := l.Start
		for it := l.FirstChild(); it != nil; it = it.NextSibling() {
			mark := "•\t"
			if l.IsOrdered() {
				mark = strconv.Itoa(i) + ".\t"
				i++
			}
			first := true
			for k := it.FirstChild(); k != nil; k = k.NextSibling() {
				switch k := k.(type) {
				case *ast.Paragraph, *ast.TextBlock:
					p := ""
					if first {
						p = mark
					}
					body.WriteString(fmt.Sprintf(`<w:p><w:pPr><w:pStyle w:val="ListParagraph"/><w:ind w:left="%d" w:hanging="360"/></w:pPr>`, 720*(depth+1)))
					body.WriteString(`<w:r><w:t xml:space="preserve">` + esc(p) + "</w:t></w:r>")
					runs(k, style{})
					body.WriteString("</w:p>")
				case *ast.List:
					list(k, depth+1)
				}
				first = false
			}
		}
	}
	var blocks func(n ast.Node, depth int)
	blocks = func(n ast.Node, depth int) {
		for c := n.FirstChild(); c != nil; c = c.NextSibling() {
			switch c := c.(type) {
			case *ast.Heading:
				para(fmt.Sprintf("Heading%d", min(c.Level, 6)), c, style{}, "")
			case *ast.Paragraph, *ast.TextBlock:
				para("", c, style{}, "")
			case *ast.List:
				list(c, depth)
			case *ast.FencedCodeBlock, *ast.CodeBlock:
				for i := 0; i < c.Lines().Len(); i++ {
					s := c.Lines().At(i)
					body.WriteString(`<w:p><w:pPr><w:pStyle w:val="Code"/></w:pPr><w:r><w:t xml:space="preserve">` + esc(strings.TrimRight(string(s.Value(src)), "\r\n")) + "</w:t></w:r></w:p>")
				}
			case *ast.Blockquote:
				for k := c.FirstChild(); k != nil; k = k.NextSibling() {
					para("Quote", k, style{i: true}, "")
				}
			case *ast.ThematicBreak:
				body.WriteString(`<w:p><w:pPr><w:pBdr><w:bottom w:val="single" w:sz="6" w:space="1" w:color="BBBBBB"/></w:pBdr></w:pPr></w:p>`)
			case *east.Table:
				body.WriteString(`<w:tbl><w:tblPr><w:tblStyle w:val="TableGrid"/><w:tblW w:w="0" w:type="auto"/><w:tblBorders>` +
					`<w:top w:val="single" w:sz="4" w:color="BBBBBB"/><w:left w:val="single" w:sz="4" w:color="BBBBBB"/><w:bottom w:val="single" w:sz="4" w:color="BBBBBB"/>` +
					`<w:right w:val="single" w:sz="4" w:color="BBBBBB"/><w:insideH w:val="single" w:sz="4" w:color="BBBBBB"/><w:insideV w:val="single" w:sz="4" w:color="BBBBBB"/></w:tblBorders></w:tblPr>`)
				for r := c.FirstChild(); r != nil; r = r.NextSibling() {
					_, head := r.(*east.TableHeader)
					body.WriteString("<w:tr>")
					for cell := r.FirstChild(); cell != nil; cell = cell.NextSibling() {
						body.WriteString("<w:tc><w:p>")
						runs(cell, style{b: head})
						body.WriteString("</w:p></w:tc>")
					}
					body.WriteString("</w:tr>")
				}
				body.WriteString("</w:tbl><w:p/>")
			case *ast.HTMLBlock:
			default:
				blocks(c, depth)
			}
		}
	}
	blocks(doc, 0)

	z := zip.NewWriter(w)
	files := []struct{ name, data string }{
		{"[Content_Types].xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/><Override PartName="/word/styles.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.styles+xml"/></Types>`},
		{"_rels/.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`},
		{"word/_rels/document.xml.rels", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/styles" Target="styles.xml"/></Relationships>`},
		{"word/styles.xml", docxStyles},
		{"word/document.xml", `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>` + body.String() + `<w:sectPr><w:pgSz w:w="11906" w:h="16838"/><w:pgMar w:top="1440" w:right="1440" w:bottom="1440" w:left="1440" w:header="708" w:footer="708" w:gutter="0"/></w:sectPr></w:body></w:document>`},
	}
	for _, f := range files {
		zw, err := z.Create(f.name)
		if err != nil {
			return err
		}
		if _, err := io.WriteString(zw, f.data); err != nil {
			return err
		}
	}
	return z.Close()
}

const docxStyles = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<w:styles xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
<w:docDefaults><w:rPrDefault><w:rPr><w:rFonts w:ascii="Calibri" w:hAnsi="Calibri" w:eastAsia="Calibri" w:cs="Calibri"/><w:sz w:val="22"/></w:rPr></w:rPrDefault><w:pPrDefault><w:pPr><w:spacing w:after="160" w:line="276" w:lineRule="auto"/></w:pPr></w:pPrDefault></w:docDefaults>
<w:style w:type="paragraph" w:default="1" w:styleId="Normal"><w:name w:val="Normal"/></w:style>
<w:style w:type="paragraph" w:styleId="Heading1"><w:name w:val="heading 1"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:pPr><w:keepNext/><w:spacing w:before="360" w:after="120"/><w:outlineLvl w:val="0"/></w:pPr><w:rPr><w:b/><w:sz w:val="40"/></w:rPr></w:style>
<w:style w:type="paragraph" w:styleId="Heading2"><w:name w:val="heading 2"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:pPr><w:keepNext/><w:spacing w:before="280" w:after="100"/><w:outlineLvl w:val="1"/></w:pPr><w:rPr><w:b/><w:sz w:val="32"/></w:rPr></w:style>
<w:style w:type="paragraph" w:styleId="Heading3"><w:name w:val="heading 3"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:pPr><w:keepNext/><w:spacing w:before="240" w:after="80"/><w:outlineLvl w:val="2"/></w:pPr><w:rPr><w:b/><w:sz w:val="27"/></w:rPr></w:style>
<w:style w:type="paragraph" w:styleId="Heading4"><w:name w:val="heading 4"/><w:basedOn w:val="Normal"/><w:next w:val="Normal"/><w:pPr><w:outlineLvl w:val="3"/></w:pPr><w:rPr><w:b/><w:sz w:val="24"/></w:rPr></w:style>
<w:style w:type="paragraph" w:styleId="Heading5"><w:name w:val="heading 5"/><w:basedOn w:val="Normal"/><w:pPr><w:outlineLvl w:val="4"/></w:pPr><w:rPr><w:b/></w:rPr></w:style>
<w:style w:type="paragraph" w:styleId="Heading6"><w:name w:val="heading 6"/><w:basedOn w:val="Normal"/><w:pPr><w:outlineLvl w:val="5"/></w:pPr><w:rPr><w:b/><w:i/></w:rPr></w:style>
<w:style w:type="paragraph" w:styleId="Code"><w:name w:val="Code"/><w:basedOn w:val="Normal"/><w:pPr><w:spacing w:after="0" w:line="240" w:lineRule="auto"/><w:shd w:val="clear" w:color="auto" w:fill="F3F4F6"/></w:pPr><w:rPr><w:rFonts w:ascii="Consolas" w:hAnsi="Consolas"/><w:sz w:val="19"/></w:rPr></w:style>
<w:style w:type="paragraph" w:styleId="ListParagraph"><w:name w:val="List Paragraph"/><w:basedOn w:val="Normal"/><w:pPr><w:spacing w:after="60"/></w:pPr></w:style>
<w:style w:type="paragraph" w:styleId="Quote"><w:name w:val="Quote"/><w:basedOn w:val="Normal"/><w:pPr><w:ind w:left="567"/></w:pPr><w:rPr><w:i/><w:color w:val="555555"/></w:rPr></w:style>
<w:style w:type="table" w:styleId="TableGrid"><w:name w:val="Table Grid"/><w:tblPr><w:tblCellMar><w:left w:w="108" w:type="dxa"/><w:right w:w="108" w:type="dxa"/></w:tblCellMar></w:tblPr></w:style>
</w:styles>`

// ---------------------------------------------------------------- DOCX / ODT / EPUB -> Markdown

func zipFile(z *zip.ReadCloser, name string) ([]byte, error) {
	for _, f := range z.File {
		if f.Name == name {
			r, err := f.Open()
			if err != nil {
				return nil, err
			}
			defer r.Close()
			return io.ReadAll(io.LimitReader(r, 256<<20))
		}
	}
	return nil, fmt.Errorf("no %s in it", name)
}

func mdEsc(s string) string {
	for _, c := range `\*_` + "`" {
		s = strings.ReplaceAll(s, string(c), `\`+string(c))
	}
	return s
}

// docxMarkdown: paragraphs with their heading styles, bold/italic runs, lists, tables.
func docxMarkdown(p string) (string, error) {
	z, err := zip.OpenReader(p)
	if err != nil {
		return "", err
	}
	defer z.Close()
	b, err := zipFile(z, "word/document.xml")
	if err != nil {
		return "", err
	}
	d := xml.NewDecoder(bytes.NewReader(b))
	var out, para strings.Builder
	var pstyle string
	var list, bold, ital, mono, inTable bool
	var row []string
	var rows [][]string
	var code []string
	flushCode := func() {
		if len(code) > 0 {
			out.WriteString("```\n" + strings.Join(code, "\n") + "\n```\n\n")
			code = nil
		}
	}
	run := func(s string) {
		if s == "" {
			return
		}
		if pstyle == "code" {
			para.WriteString(s)
			return
		}
		switch {
		case mono:
			para.WriteString("`" + s + "`")
		case bold && ital:
			para.WriteString("***" + mdEsc(s) + "***")
		case bold:
			para.WriteString("**" + mdEsc(s) + "**")
		case ital:
			para.WriteString("*" + mdEsc(s) + "*")
		default:
			para.WriteString(mdEsc(s))
		}
	}
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			attr := func(n string) string {
				for _, a := range t.Attr {
					if a.Name.Local == n {
						return a.Value
					}
				}
				return ""
			}
			switch t.Name.Local {
			case "p":
				para.Reset()
				pstyle, list = "", false
			case "pStyle":
				pstyle = strings.ToLower(attr("val"))
			case "numPr":
				list = true
			case "r":
				bold, ital, mono = false, false, false
			case "rFonts":
				f := strings.ToLower(attr("ascii"))
				mono = strings.Contains(f, "consolas") || strings.Contains(f, "courier") || strings.Contains(f, "mono")
			case "b":
				bold = attr("val") != "0" && attr("val") != "false"
			case "i":
				ital = attr("val") != "0" && attr("val") != "false"
			case "t":
				var s string
				d.DecodeElement(&s, &t)
				run(s)
			case "tab":
				para.WriteString(" ")
			case "br", "cr":
				para.WriteString("  \n")
			case "tbl":
				inTable, rows = true, nil
			case "tr":
				row = nil
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "p":
				if pstyle == "code" && !inTable {
					code = append(code, para.String())
					continue
				}
				flushCode()
				s := strings.TrimSpace(para.String())
				if inTable {
					row = append(row, strings.ReplaceAll(s, "|", `\|`))
					continue
				}
				if s == "" {
					continue
				}
				// the list marker Word shows as text (ours, or typed): "•\t", "-\t", "1.\t"
				if m := listMark.FindStringSubmatch(s); m != nil {
					if m[1] != "" {
						out.WriteString(m[1] + ". " + s[len(m[0]):] + "\n")
					} else {
						out.WriteString("- " + s[len(m[0]):] + "\n")
					}
					continue
				}
				switch {
				case strings.HasPrefix(pstyle, "heading") || strings.HasPrefix(pstyle, "nagwek") || strings.HasPrefix(pstyle, "nagłówek"):
					n, _ := strconv.Atoi(strings.TrimLeft(pstyle, "headingnagwkłó "))
					out.WriteString(strings.Repeat("#", max(1, min(n, 6))) + " " + strings.Trim(s, "*") + "\n\n")
				case pstyle == "title" || pstyle == "tytuł":
					out.WriteString("# " + strings.Trim(s, "*") + "\n\n")
				case list || strings.Contains(pstyle, "list"):
					out.WriteString("- " + s + "\n")
				default:
					out.WriteString(s + "\n\n")
				}
			case "tr":
				if inTable {
					rows = append(rows, row)
				}
			case "tbl":
				inTable = false
				out.WriteString(mdTable(rows) + "\n")
			}
		}
	}
	flushCode()
	return out.String(), nil
}

var listMark = regexp.MustCompile(`^(?:[•·▪◦‣\-–]|(\d+)[.)])\s+`)

func mdTable(rows [][]string) string {
	if len(rows) == 0 {
		return ""
	}
	n := 0
	for _, r := range rows {
		n = max(n, len(r))
	}
	var b strings.Builder
	for i, r := range rows {
		for len(r) < n {
			r = append(r, "")
		}
		b.WriteString("| " + strings.Join(r, " | ") + " |\n")
		if i == 0 {
			b.WriteString("|" + strings.Repeat(" --- |", n) + "\n")
		}
	}
	return b.String()
}

// odtMarkdown: OpenDocument text (LibreOffice): headings, paragraphs, lists.
func odtMarkdown(p string) (string, error) {
	z, err := zip.OpenReader(p)
	if err != nil {
		return "", err
	}
	defer z.Close()
	b, err := zipFile(z, "content.xml")
	if err != nil {
		return "", err
	}
	d := xml.NewDecoder(bytes.NewReader(b))
	var out, para strings.Builder
	type blk struct {
		kind  string
		level int
	}
	var stack []blk
	listDepth := 0
	for {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "h":
				lvl := 1
				for _, a := range t.Attr {
					if a.Name.Local == "outline-level" {
						lvl, _ = strconv.Atoi(a.Value)
					}
				}
				para.Reset()
				stack = append(stack, blk{"h", lvl})
			case "p":
				para.Reset()
				stack = append(stack, blk{"p", 0})
			case "list":
				listDepth++
			case "s":
				para.WriteString(" ")
			case "tab":
				para.WriteString(" ")
			case "line-break":
				para.WriteString("  \n")
			}
		case xml.CharData:
			if len(stack) > 0 {
				para.WriteString(mdEsc(string(t)))
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "h", "p":
				if len(stack) == 0 {
					continue
				}
				top := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				s := strings.TrimSpace(para.String())
				para.Reset()
				if s == "" {
					continue
				}
				switch {
				case top.kind == "h":
					out.WriteString(strings.Repeat("#", max(1, min(top.level, 6))) + " " + s + "\n\n")
				case listDepth > 0:
					out.WriteString(strings.Repeat("  ", listDepth-1) + "- " + s + "\n")
				default:
					out.WriteString(s + "\n\n")
				}
			case "list":
				listDepth--
				if listDepth == 0 {
					out.WriteString("\n")
				}
			}
		}
	}
	return out.String(), nil
}

// epubMarkdown: the book's chapters in reading order.
func epubMarkdown(p string) (string, string, error) {
	z, err := zip.OpenReader(p)
	if err != nil {
		return "", "", err
	}
	defer z.Close()
	c, err := zipFile(z, "META-INF/container.xml")
	if err != nil {
		return "", "", err
	}
	var cont struct {
		Rootfiles []struct {
			Path string `xml:"full-path,attr"`
		} `xml:"rootfiles>rootfile"`
	}
	if err := xml.Unmarshal(c, &cont); err != nil || len(cont.Rootfiles) == 0 {
		return "", "", errors.New("not an e-book")
	}
	opfPath := cont.Rootfiles[0].Path
	ob, err := zipFile(z, opfPath)
	if err != nil {
		return "", "", err
	}
	var opf struct {
		Title    string `xml:"metadata>title"`
		Manifest []struct {
			ID   string `xml:"id,attr"`
			Href string `xml:"href,attr"`
		} `xml:"manifest>item"`
		Spine []struct {
			IDRef string `xml:"idref,attr"`
		} `xml:"spine>itemref"`
	}
	if err := xml.Unmarshal(ob, &opf); err != nil {
		return "", "", err
	}
	href := map[string]string{}
	for _, it := range opf.Manifest {
		href[it.ID] = it.Href
	}
	var out strings.Builder
	base := path.Dir(opfPath)
	for _, s := range opf.Spine {
		h := href[s.IDRef]
		if h == "" {
			continue
		}
		name := path.Clean(path.Join(base, h))
		b, err := zipFile(z, name)
		if err != nil {
			continue
		}
		m, err := htmltomarkdown.ConvertString(string(b))
		if err != nil {
			continue
		}
		out.WriteString(strings.TrimSpace(m) + "\n\n")
	}
	title := strings.TrimSpace(opf.Title)
	if title == "" {
		title = stem(p)
	}
	return out.String(), title, nil
}

// writeEPUB: an EPUB 3 book of one chapter.
func writeEPUB(w io.Writer, title, body string) error {
	z := zip.NewWriter(w)
	// the mimetype comes first and uncompressed
	mt, err := z.CreateHeader(&zip.FileHeader{Name: "mimetype", Method: zip.Store})
	if err != nil {
		return err
	}
	io.WriteString(mt, "application/epub+zip")
	t := html.EscapeString(title)
	id := fmt.Sprintf("urn:mw4w:%d", time.Now().UnixNano())
	files := []struct{ name, data string }{
		{"META-INF/container.xml", `<?xml version="1.0" encoding="UTF-8"?>
<container version="1.0" xmlns="urn:oasis:names:tc:opendocument:xmlns:container"><rootfiles><rootfile full-path="OEBPS/content.opf" media-type="application/oebps-package+xml"/></rootfiles></container>`},
		{"OEBPS/content.opf", `<?xml version="1.0" encoding="UTF-8"?>
<package xmlns="http://www.idpf.org/2007/opf" version="3.0" unique-identifier="id"><metadata xmlns:dc="http://purl.org/dc/elements/1.1/"><dc:identifier id="id">` + id + `</dc:identifier><dc:title>` + t + `</dc:title><dc:language>en</dc:language><meta property="dcterms:modified">` + time.Now().UTC().Format("2006-01-02T15:04:05Z") + `</meta></metadata>
<manifest><item id="nav" href="nav.xhtml" media-type="application/xhtml+xml" properties="nav"/><item id="c1" href="chapter.xhtml" media-type="application/xhtml+xml"/></manifest><spine><itemref idref="c1"/></spine></package>`},
		{"OEBPS/nav.xhtml", `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml" xmlns:epub="http://www.idpf.org/2007/ops"><head><title>` + t + `</title></head><body><nav epub:type="toc"><ol><li><a href="chapter.xhtml">` + t + `</a></li></ol></nav></body></html>`},
		{"OEBPS/chapter.xhtml", `<?xml version="1.0" encoding="UTF-8"?>
<html xmlns="http://www.w3.org/1999/xhtml"><head><title>` + t + `</title></head><body>` + body + `</body></html>`},
	}
	for _, f := range files {
		fw, err := z.Create(f.name)
		if err != nil {
			return err
		}
		if _, err := io.WriteString(fw, f.data); err != nil {
			return err
		}
	}
	return z.Close()
}
