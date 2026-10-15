package main

// Documents previewed as their text: Word, PowerPoint and Excel files
// (.docx .pptx .xlsx) and their OpenDocument cousins (.odt .odp .ods) are
// zips of XML, read with archive/zip and encoding/xml; spreadsheets show
// their first sheet as a table. RTF is reduced to its plain text. Built off
// the UI goroutine (kindDoc), since a document can be large.

import (
	"archive/zip"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"
)

const (
	docMaxXML     = 16 << 20 // bytes read from one XML part
	docMaxRows    = 500      // spreadsheet rows shown
	docMaxCols    = 26
	docCellWidth  = 24 // a spreadsheet column is cut to this
	rtfMaxBytes   = 4 << 20
	docMaxSlides  = 200
	odsMaxRepeats = 64 // a repeated cell or row is shown at most this often
)

var docExts = setOf(".docx", ".docm", ".pptx", ".xlsx", ".xlsm", ".odt", ".odp", ".ods", ".rtf")

// docPreview shows a document's text, or says why it can't.
func docPreview(p preview, path string, e entry, o previewOpts) preview {
	ext := strings.ToLower(filepath.Ext(e.name))
	lines, meta, err := docText(path, ext)
	if err != nil {
		p.meta = humanSize(e.size)
		p.lines = []string{"  cannot read the document: " + errText(err)}
		return p
	}
	p.meta = meta + " · " + humanSize(e.size)
	if len(lines) == 0 {
		lines = []string{"(no text)"}
	}
	for i, l := range lines {
		lines[i] = "  " + expandTabs(sanitize(l))
	}
	p.lines = fitLines(lines, o, previewMaxLines)
	return p
}

func docText(path, ext string) ([]string, string, error) {
	if ext == ".rtf" {
		f, err := os.Open(path)
		if err != nil {
			return nil, "", err
		}
		defer f.Close()
		b, err := io.ReadAll(io.LimitReader(f, rtfMaxBytes))
		if err != nil {
			return nil, "", err
		}
		return rtfText(string(b)), "RTF document", nil
	}
	z, err := zip.OpenReader(path)
	if err != nil {
		return nil, "", err
	}
	defer z.Close()
	switch ext {
	case ".docx", ".docm":
		lines, err := ooxmlParagraphs(&z.Reader, "word/document.xml")
		return lines, "Word document", err
	case ".pptx":
		return pptxText(&z.Reader)
	case ".xlsx", ".xlsm":
		return xlsxText(&z.Reader)
	case ".odt":
		lines, err := odfText(&z.Reader, false)
		return lines, "OpenDocument text", err
	case ".odp":
		lines, err := odfText(&z.Reader, true)
		return lines, "OpenDocument presentation", err
	case ".ods":
		return odsText(&z.Reader)
	}
	return nil, "", errors.New("unknown document type")
}

func openPart(z *zip.Reader, name string) (io.ReadCloser, error) {
	for _, f := range z.File {
		if f.Name == name {
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			return struct {
				io.Reader
				io.Closer
			}{io.LimitReader(rc, docMaxXML), rc}, nil
		}
	}
	return nil, fmt.Errorf("%s is missing", name)
}

// ooxmlParagraphs reads the text of a Word part: w:t runs, w:p paragraphs,
// tabs and breaks; table cells are joined with " │ ".
func ooxmlParagraphs(z *zip.Reader, name string) ([]string, error) {
	rc, err := openPart(z, name)
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	var lines, cells []string
	var cur, cell strings.Builder
	inText, inCell := false, 0
	d := xml.NewDecoder(rc)
	for len(lines) < previewMaxLines {
		tok, err := d.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return lines, nil // a damaged end: what was read so far
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				inText = true
			case "tab":
				cur.WriteString("\t")
			case "br", "cr":
				lines = append(lines, cur.String())
				cur.Reset()
			case "tc":
				inCell++
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				if inCell > 0 {
					if cur.Len() > 0 {
						if cell.Len() > 0 {
							cell.WriteString(" ")
						}
						cell.WriteString(cur.String())
					}
				} else {
					lines = append(lines, cur.String())
				}
				cur.Reset()
			case "tc":
				inCell--
				cells = append(cells, cell.String())
				cell.Reset()
			case "tr":
				lines = append(lines, strings.Join(cells, " │ "))
				cells = nil
			}
		case xml.CharData:
			if inText {
				cur.Write(t)
			}
		}
	}
	return trimBlankRuns(lines), nil
}

var slideNum = regexp.MustCompile(`^ppt/slides/slide(\d+)\.xml$`)

func pptxText(z *zip.Reader) ([]string, string, error) {
	type slide struct {
		n    int
		name string
	}
	var slides []slide
	for _, f := range z.File {
		if m := slideNum.FindStringSubmatch(f.Name); m != nil {
			n, _ := strconv.Atoi(m[1])
			slides = append(slides, slide{n, f.Name})
		}
	}
	if len(slides) == 0 {
		return nil, "", errors.New("no slides")
	}
	sort.Slice(slides, func(a, b int) bool { return slides[a].n < slides[b].n })
	var lines []string
	for i, s := range slides {
		if i >= docMaxSlides || len(lines) >= previewMaxLines {
			break
		}
		text, err := ooxmlParagraphs(z, s.name)
		if err != nil {
			continue
		}
		if i > 0 {
			lines = append(lines, "")
		}
		lines = append(lines, fmt.Sprintf("── Slide %d ──", s.n))
		lines = append(lines, nonBlank(text)...)
	}
	return lines, fmt.Sprintf("PowerPoint · %s", plural(len(slides), "slide")), nil
}

func xlsxText(z *zip.Reader) ([]string, string, error) {
	var shared []string
	if rc, err := openPart(z, "xl/sharedStrings.xml"); err == nil {
		d := xml.NewDecoder(rc)
		var cur strings.Builder
		inT := false
		for {
			tok, err := d.Token()
			if err != nil {
				break
			}
			switch t := tok.(type) {
			case xml.StartElement:
				if t.Name.Local == "t" {
					inT = true
				}
			case xml.EndElement:
				switch t.Name.Local {
				case "t":
					inT = false
				case "si":
					shared = append(shared, cur.String())
					cur.Reset()
				}
			case xml.CharData:
				if inT {
					cur.Write(t)
				}
			}
		}
		rc.Close()
	}
	sheets := xlsxSheetNames(z)
	rc, err := openPart(z, "xl/worksheets/sheet1.xml")
	if err != nil {
		return nil, "", err
	}
	defer rc.Close()
	var rows [][]string
	d := xml.NewDecoder(rc)
	var row []string
	var col int
	var typ string
	var val strings.Builder
	inV := false
	for len(rows) < docMaxRows {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "row":
				row = nil
			case "c":
				typ, col = "", len(row)
				val.Reset()
				for _, a := range t.Attr {
					switch a.Name.Local {
					case "t":
						typ = a.Value
					case "r":
						col = colIndex(a.Value)
					}
				}
			case "v", "t":
				inV = true
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "v", "t":
				inV = false
			case "c":
				v := val.String()
				switch typ {
				case "s":
					if i, err := strconv.Atoi(v); err == nil && i >= 0 && i < len(shared) {
						v = shared[i]
					}
				case "b":
					v = map[string]string{"0": "FALSE", "1": "TRUE"}[v]
				}
				if col < docMaxCols {
					for len(row) <= col {
						row = append(row, "")
					}
					row[col] = v
				}
			case "row":
				rows = append(rows, row)
			}
		case xml.CharData:
			if inV {
				val.Write(t)
			}
		}
	}
	meta := "Excel"
	if len(sheets) > 0 {
		meta += " · " + plural(len(sheets), "sheet")
	}
	lines := table(rows)
	if len(sheets) > 0 {
		lines = append([]string{"── " + sheets[0] + " ──"}, lines...)
	}
	return lines, meta, nil
}

func xlsxSheetNames(z *zip.Reader) []string {
	rc, err := openPart(z, "xl/workbook.xml")
	if err != nil {
		return nil
	}
	defer rc.Close()
	var names []string
	d := xml.NewDecoder(rc)
	for {
		tok, err := d.Token()
		if err != nil {
			return names
		}
		if t, ok := tok.(xml.StartElement); ok && t.Name.Local == "sheet" {
			for _, a := range t.Attr {
				if a.Name.Local == "name" {
					names = append(names, a.Value)
				}
			}
		}
	}
}

// colIndex turns a cell reference's letters into a column: "C7" → 2.
func colIndex(ref string) int {
	n := 0
	for _, c := range ref {
		if c < 'A' || c > 'Z' {
			break
		}
		n = n*26 + int(c-'A'+1)
	}
	return max(0, n-1)
}

// odfText reads an OpenDocument text or presentation's paragraphs.
func odfText(z *zip.Reader, slides bool) ([]string, error) {
	rc, err := openPart(z, "content.xml")
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	var lines []string
	var cur strings.Builder
	depth, page := 0, 0 // depth: inside text:p / text:h
	d := xml.NewDecoder(rc)
	for len(lines) < previewMaxLines {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "page":
				if slides {
					page++
					if page > 1 {
						lines = append(lines, "")
					}
					lines = append(lines, fmt.Sprintf("── Slide %d ──", page))
				}
			case "p", "h":
				depth++
			case "s":
				n := 1
				for _, a := range t.Attr {
					if a.Name.Local == "c" {
						n, _ = strconv.Atoi(a.Value)
					}
				}
				cur.WriteString(strings.Repeat(" ", max(1, min(n, 80))))
			case "tab":
				cur.WriteString("\t")
			case "line-break":
				lines = append(lines, cur.String())
				cur.Reset()
			}
		case xml.EndElement:
			if t.Name.Local == "p" || t.Name.Local == "h" {
				depth--
				if depth == 0 {
					lines = append(lines, cur.String())
					cur.Reset()
				}
			}
		case xml.CharData:
			if depth > 0 {
				cur.Write(t)
			}
		}
	}
	if slides {
		return lines, nil
	}
	return trimBlankRuns(lines), nil
}

// odsText reads the first sheet of an OpenDocument spreadsheet.
func odsText(z *zip.Reader) ([]string, string, error) {
	rc, err := openPart(z, "content.xml")
	if err != nil {
		return nil, "", err
	}
	defer rc.Close()
	var rows [][]string
	var row []string
	var cell strings.Builder
	var sheets []string
	repeat, rowRepeat, inCell := 1, 1, 0
	d := xml.NewDecoder(rc)
	for {
		tok, err := d.Token()
		if err != nil {
			break
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "table":
				for _, a := range t.Attr {
					if a.Name.Local == "name" {
						sheets = append(sheets, a.Value)
					}
				}
			case "table-row":
				row, rowRepeat = nil, attrInt(t, "number-rows-repeated")
			case "table-cell", "covered-table-cell":
				inCell++
				cell.Reset()
				repeat = attrInt(t, "number-columns-repeated")
			case "p":
				if inCell > 0 && cell.Len() > 0 {
					cell.WriteString(" ")
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "table-cell", "covered-table-cell":
				inCell--
				for i := 0; i < min(repeat, odsMaxRepeats) && len(row) < docMaxCols; i++ {
					row = append(row, cell.String())
				}
			case "table-row":
				if len(sheets) == 1 && len(rows) < docMaxRows {
					for i := 0; i < min(rowRepeat, odsMaxRepeats); i++ {
						rows = append(rows, row)
					}
				}
			}
		case xml.CharData:
			if inCell > 0 && len(sheets) == 1 {
				cell.Write(t)
			}
		}
	}
	if len(sheets) == 0 {
		return nil, "", errors.New("no sheets")
	}
	return append([]string{"── " + sheets[0] + " ──"}, table(rows)...), "OpenDocument spreadsheet · " + plural(len(sheets), "sheet"), nil
}

func attrInt(t xml.StartElement, name string) int {
	for _, a := range t.Attr {
		if a.Name.Local == name {
			if n, err := strconv.Atoi(a.Value); err == nil && n > 0 {
				return n
			}
		}
	}
	return 1
}

// table lines up rows in columns, dropping empty trailing rows and columns.
func table(rows [][]string) []string {
	for len(rows) > 0 && strings.Join(rows[len(rows)-1], "") == "" {
		rows = rows[:len(rows)-1]
	}
	cols := 0
	for _, r := range rows {
		for c := len(r) - 1; c >= 0; c-- {
			if r[c] != "" {
				cols = max(cols, c+1)
				break
			}
		}
	}
	width := make([]int, cols)
	cut := func(s string) string {
		s = strings.Join(strings.Fields(s), " ")
		if utf8.RuneCountInString(s) > docCellWidth {
			s = string([]rune(s)[:docCellWidth-1]) + "…"
		}
		return s
	}
	for _, r := range rows {
		for c := 0; c < min(cols, len(r)); c++ {
			width[c] = max(width[c], utf8.RuneCountInString(cut(r[c])))
		}
	}
	var out []string
	for _, r := range rows {
		var b strings.Builder
		for c := 0; c < cols; c++ {
			v := ""
			if c < len(r) {
				v = cut(r[c])
			}
			if c > 0 {
				b.WriteString(" │ ")
			}
			b.WriteString(v + strings.Repeat(" ", width[c]-utf8.RuneCountInString(v)))
		}
		line := b.String()
		for strings.HasSuffix(line, " ") || strings.HasSuffix(line, " │") {
			line = strings.TrimSuffix(strings.TrimRight(line, " "), " │")
		}
		out = append(out, line)
	}
	return out
}

// trimBlankRuns keeps at most one blank line in a row.
func trimBlankRuns(lines []string) []string {
	var out []string
	for _, l := range lines {
		if strings.TrimSpace(l) == "" && (len(out) == 0 || out[len(out)-1] == "") {
			continue
		}
		if strings.TrimSpace(l) == "" {
			l = ""
		}
		out = append(out, l)
	}
	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	return out
}

func nonBlank(lines []string) []string {
	var out []string
	for _, l := range lines {
		if strings.TrimSpace(l) != "" {
			out = append(out, l)
		}
	}
	return out
}

// rtfSkip are RTF destinations that hold no document text.
var rtfSkip = setOf("fonttbl", "colortbl", "stylesheet", "info", "pict", "object", "header", "footer",
	"headerl", "headerr", "footerl", "footerr", "themedata", "colorschememapping", "datastore",
	"latentstyles", "listtable", "listoverridetable", "rsidtbl", "generator", "xmlnstbl", "mmathPr", "fldinst")

var rtfToken = regexp.MustCompile(`\\([a-zA-Z]+)(-?\d+)? ?|\\'([0-9a-fA-F]{2})|\\(.)|([{}])|([^\\{}\r\n]+)|[\r\n]+`)

// rtfText reduces RTF to its plain text, one paragraph per line.
func rtfText(src string) []string {
	type group struct {
		skip bool
		uc   int // characters to skip after a \u
	}
	stack := []group{{uc: 1}}
	top := func() *group { return &stack[len(stack)-1] }
	var b strings.Builder
	pendingSkip := 0  // fallback characters still to skip after \u
	newGroup := false // the next control word is the group's first
	for _, m := range rtfToken.FindAllStringSubmatch(src, -1) {
		g := top()
		switch {
		case m[5] == "{":
			stack = append(stack, *g)
			newGroup = true
			continue
		case m[5] == "}":
			if len(stack) > 1 {
				stack = stack[:len(stack)-1]
			}
		case m[1] != "":
			word, arg := m[1], m[2]
			if newGroup && rtfSkip[word] {
				g.skip = true
			}
			if g.skip {
				break
			}
			switch word {
			case "par", "line", "row", "sect", "page":
				b.WriteString("\n")
			case "tab", "cell":
				b.WriteString("\t")
			case "uc":
				g.uc, _ = strconv.Atoi(arg)
			case "u":
				n, _ := strconv.Atoi(arg)
				if n < 0 {
					n += 65536
				}
				b.WriteRune(rune(n))
				pendingSkip = g.uc
			case "emdash":
				b.WriteString("—")
			case "endash":
				b.WriteString("–")
			case "bullet":
				b.WriteString("•")
			case "lquote":
				b.WriteString("‘")
			case "rquote":
				b.WriteString("’")
			case "ldblquote":
				b.WriteString("“")
			case "rdblquote":
				b.WriteString("”")
			}
		case m[3] != "":
			if pendingSkip > 0 {
				pendingSkip--
				break
			}
			if !g.skip {
				c, _ := strconv.ParseUint(m[3], 16, 8)
				b.WriteString(latin1([]byte{byte(c)}))
			}
		case m[4] != "":
			if m[4] == "*" && newGroup {
				g.skip = true
			} else if !g.skip && (m[4] == "\\" || m[4] == "{" || m[4] == "}") {
				b.WriteString(m[4])
			} else if !g.skip && m[4] == "~" {
				b.WriteString(" ")
			}
		case m[6] != "":
			text := m[6]
			if pendingSkip > 0 {
				k := min(pendingSkip, utf8.RuneCountInString(text))
				text = string([]rune(text)[k:])
				pendingSkip -= k
			}
			if !g.skip {
				b.WriteString(text)
			}
		}
		newGroup = false
	}
	return trimBlankRuns(strings.Split(b.String(), "\n"))
}
