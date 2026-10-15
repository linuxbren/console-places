package main

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func mkzip(t *testing.T, path string, parts map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	z := zip.NewWriter(f)
	for name, body := range parts {
		w, _ := z.Create(name)
		w.Write([]byte(body))
	}
	z.Close()
	f.Close()
}

func TestDocText(t *testing.T) {
	dir := t.TempDir()
	mkzip(t, filepath.Join(dir, "a.docx"), map[string]string{"word/document.xml": `<w:document xmlns:w="w"><w:body>
<w:p><w:r><w:t>Hello </w:t></w:r><w:r><w:t>world</w:t></w:r></w:p>
<w:p><w:r><w:tab/><w:t>indented</w:t></w:r></w:p>
<w:tbl><w:tr><w:tc><w:p><w:r><w:t>A1</w:t></w:r></w:p></w:tc><w:tc><w:p><w:r><w:t>B1</w:t></w:r></w:p></w:tc></w:tr></w:tbl>
</w:body></w:document>`})
	mkzip(t, filepath.Join(dir, "b.pptx"), map[string]string{
		"ppt/slides/slide2.xml":  `<p:sld xmlns:a="a" xmlns:p="p"><a:p><a:r><a:t>Second</a:t></a:r></a:p></p:sld>`,
		"ppt/slides/slide10.xml": `<p:sld xmlns:a="a" xmlns:p="p"><a:p><a:r><a:t>Tenth</a:t></a:r></a:p></p:sld>`,
		"ppt/slides/slide1.xml":  `<p:sld xmlns:a="a" xmlns:p="p"><a:p><a:r><a:t>Title</a:t></a:r></a:p><a:p><a:r><a:t>Point</a:t></a:r></a:p></p:sld>`,
	})
	mkzip(t, filepath.Join(dir, "c.xlsx"), map[string]string{
		"xl/workbook.xml":          `<workbook><sheets><sheet name="Budget"/><sheet name="Notes"/></sheets></workbook>`,
		"xl/sharedStrings.xml":     `<sst><si><t>Item</t></si><si><t>Cost</t></si><si><r><t>Coff</t></r><r><t>ee</t></r></si></sst>`,
		"xl/worksheets/sheet1.xml": `<worksheet><sheetData><row r="1"><c r="A1" t="s"><v>0</v></c><c r="B1" t="s"><v>1</v></c></row><row r="2"><c r="A2" t="s"><v>2</v></c><c r="C2"><v>3.5</v></c></row></sheetData></worksheet>`,
	})
	mkzip(t, filepath.Join(dir, "d.ods"), map[string]string{"content.xml": `<office:document-content xmlns:office="o" xmlns:table="t" xmlns:text="x"><office:body><office:spreadsheet>
<table:table table:name="Sheet1"><table:table-row><table:table-cell><text:p>x</text:p></table:table-cell><table:table-cell table:number-columns-repeated="2"><text:p>y</text:p></table:table-cell></table:table-row></table:table>
<table:table table:name="Other"><table:table-row><table:table-cell><text:p>no</text:p></table:table-cell></table:table-row></table:table>
</office:spreadsheet></office:body></office:document-content>`})
	mkzip(t, filepath.Join(dir, "e.odp"), map[string]string{"content.xml": `<office:document-content xmlns:office="o" xmlns:draw="d" xmlns:text="x"><office:body><office:presentation>
<draw:page><draw:frame><draw:text-box><text:p>Welcome</text:p></draw:text-box></draw:frame></draw:page>
<draw:page><draw:frame><draw:text-box><text:p>Bye<text:s text:c="2"/>now</text:p></draw:text-box></draw:frame></draw:page>
</office:presentation></office:body></office:document-content>`})
	os.WriteFile(filepath.Join(dir, "f.rtf"), []byte(`{\rtf1\ansi\deff0{\fonttbl{\f0 Times;}}{\colortbl;\red255\green0\blue0;}{\*\generator Test;}
{\info{\title Secret title}}\pard Caf\'e9 \b bold\b0\par
Second line \u8364? euro\tab tab\par
{\*\unknown skipped}Last \{braces\}}`), 0o644)

	for name, want := range map[string]string{
		"a.docx": "Hello world\n\tindented\nA1 │ B1",
		"b.pptx": "── Slide 1 ──\nTitle\nPoint\n\n── Slide 2 ──\nSecond\n\n── Slide 10 ──\nTenth",
		"c.xlsx": "── Budget ──\nItem   │ Cost\nCoffee │      │ 3.5",
		"d.ods":  "── Sheet1 ──\nx │ y │ y",
		"e.odp":  "── Slide 1 ──\nWelcome\n\n── Slide 2 ──\nBye  now",
		"f.rtf":  "Café bold\nSecond line € euro\ttab\nLast {braces}",
	} {
		lines, _, err := docText(filepath.Join(dir, name), filepath.Ext(name))
		if got := strings.Join(lines, "\n"); err != nil || got != want {
			t.Errorf("%s: %v\n got %q\nwant %q", name, err, got, want)
		}
	}

	m := testModel(t, dir)
	for i, e := range m.entries {
		if e.name == "c.xlsx" {
			m.cursor = i
		}
	}
	m.refreshPreview() // async: the placeholder, then the build in the background
	nm, _ := m.Update(m.buildPreviewCmd(m.pvKey)())
	m = nm.(model)
	if pv := ansi.Strip(strings.Join(m.pv.lines, "\n")); !strings.Contains(pv, "Coffee") || !strings.Contains(m.pv.meta, "Excel · 2 sheets") {
		t.Fatalf("preview %q %q loading=%v:\n%s", m.pv.title, m.pv.meta, m.pv.loading, pv)
	}
	os.WriteFile(filepath.Join(dir, "broken.docx"), []byte("not a zip"), 0o644)
	if _, _, err := docText(filepath.Join(dir, "broken.docx"), ".docx"); err == nil {
		t.Fatal("a broken document reads")
	}
}
