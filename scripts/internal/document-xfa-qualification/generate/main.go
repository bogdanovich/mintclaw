// Command generate creates the synthetic PDF4A XFA qualification corpus.
//
// The files are deliberately generated outside pkg/document/testdata: PDF4A is
// a feasibility gate, not production XFA support. Keeping the corpus here lets
// the qualification remain reproducible without advertising these documents as
// supported runtime inputs.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

type pdfObject []byte

type fixture struct {
	name    string
	objects []pdfObject
}

type manifest struct {
	SchemaVersion string            `json:"schema_version"`
	Privacy       string            `json:"privacy"`
	License       string            `json:"license"`
	Fixtures      []manifestFixture `json:"fixtures"`
}

type manifestFixture struct {
	ID           string `json:"id"`
	File         string `json:"file"`
	SHA256       string `json:"sha256"`
	Construction string `json:"construction"`
	License      string `json:"license"`
	Class        string `json:"class"`
	ExpectedGate string `json:"expected_gate"`
}

func main() {
	output := flag.String("output", "", "directory for generated fixtures")
	flag.Parse()
	if *output == "" {
		fatalf("--output is required")
	}
	if err := os.MkdirAll(*output, 0o755); err != nil {
		fatalf("create output directory: %v", err)
	}

	fixtures := []fixture{
		xfaFixture("static.pdf", "forbidden", false, false, false, false, false),
		xfaFixture("foreground.pdf", "forbidden", false, false, false, true, false),
		xfaFixture("dynamic.pdf", "required", false, false, false, false, false),
		xfaFixture("repeating.pdf", "forbidden", false, false, false, false, true),
		xfaFixture("scripted.pdf", "forbidden", true, false, false, false, false),
		xfaFixture("malformed.pdf", "forbidden", false, true, false, false, false),
		xfaFixture("signed-restricted.pdf", "forbidden", false, false, true, false, false),
	}
	result := manifest{
		SchemaVersion: "mintclaw.pdf4a.xfa_fixture_manifest.v1",
		Privacy:       "synthetic_only",
		License:       "MIT (MintClaw repository)",
	}
	for _, item := range fixtures {
		data := encodePDF(item.objects)
		path := filepath.Join(*output, item.name)
		if err := os.WriteFile(path, data, 0o644); err != nil {
			fatalf("write %s: %v", path, err)
		}
		digest := sha256.Sum256(data)
		entry := manifestFixture{
			ID:           item.name[:len(item.name)-len(".pdf")],
			File:         item.name,
			SHA256:       hex.EncodeToString(digest[:]),
			Construction: "deterministic_go_generator",
			License:      "MIT (MintClaw repository)",
			Class:        "pure_static_xfa",
			ExpectedGate: "candidate",
		}
		switch item.name {
		case "foreground.pdf":
			entry.Class = "foreground_static_xfa"
			entry.ExpectedGate = "refuse_foreground"
		case "dynamic.pdf":
			entry.Class = "pure_dynamic_xfa"
			entry.ExpectedGate = "refuse_dynamic"
		case "repeating.pdf":
			entry.Class = "repeating_static_xfa"
			entry.ExpectedGate = "refuse_repeating_or_page_growth"
		case "scripted.pdf":
			entry.Class = "script_dependent_static_xfa"
			entry.ExpectedGate = "refuse_script"
		case "malformed.pdf":
			entry.Class = "malformed_static_xfa"
			entry.ExpectedGate = "refuse_malformed"
		case "signed-restricted.pdf":
			entry.Class = "signed_restricted_static_xfa"
			entry.ExpectedGate = "refuse_signature_or_restriction"
		}
		result.Fixtures = append(result.Fixtures, entry)
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fatalf("encode manifest: %v", err)
	}
	encoded = append(encoded, '\n')
	if err := os.WriteFile(filepath.Join(*output, "manifest.json"), encoded, 0o644); err != nil {
		fatalf("write manifest: %v", err)
	}
}

func xfaFixture(name, dynamicRender string, scripted, malformed, signed, foreground, repeating bool) fixture {
	preamble := `<?xml version="1.0" encoding="UTF-8"?><xdp:xdp xmlns:xdp="http://ns.adobe.com/xdp/">`
	config := fmt.Sprintf(
		`<config xmlns="http://www.xfa.org/schema/xci/3.3/"><present><pdf><dynamicRender>%s</dynamicRender></pdf></present></config>`,
		dynamicRender,
	)
	event := ""
	if scripted {
		event = `<event activity="initialize"><script contentType="application/x-javascript">xfa.host.gotoURL("https://qualification.invalid/")</script></event>`
	}
	occur := ""
	if repeating {
		occur = `<occur min="1" max="2" initial="1"/>`
	}
	template := fmt.Sprintf(
		`<template xmlns="http://www.xfa.org/schema/xfa-template/3.3">`+
			`<subform name="root" mergeMode="matchTemplate" layout="position">`+
			`<pageSet><pageArea name="page1"><medium short="612pt" long="792pt"/>`+
			`<contentArea x="0pt" y="0pt" w="612pt" h="792pt"/></pageArea></pageSet>`+
			`<subform name="first" x="0pt" y="0pt" w="612pt" h="792pt" layout="position">%s`+
			`<draw name="title" x="72pt" y="72pt" w="360pt" h="24pt">`+
			`<font size="16pt" weight="bold"/><value><text>MINTCLAW_XFA_STATIC</text></value></draw>`+
			`<field name="hello" x="72pt" y="120pt" w="360pt" h="32pt">%s`+
			`<caption placement="top"><value><text>Qualification value</text></value></caption>`+
			`<ui><textEdit multiLine="0"/></ui><value><text/></value></field>`+
			`</subform></subform></template>`,
		occur,
		event,
	)
	datasets := `<xfa:datasets xmlns:xfa="http://www.xfa.org/schema/xfa-data/1.0/"><xfa:data>` +
		`<qualification><first><hello>MINTCLAW_XFA_BEFORE</hello></first></qualification>` +
		`</xfa:data></xfa:datasets>`
	if malformed {
		datasets = `<xfa:datasets xmlns:xfa="http://www.xfa.org/schema/xfa-data/1.0/"><xfa:data><qualification>`
	}
	postamble := `</xdp:xdp>`

	fields := "[]"
	objects := []pdfObject{
		rawObject("<< /Type /Catalog /Pages 2 0 R /NeedsRendering true /AcroForm 5 0 R >>"),
		rawObject("<< /Type /Pages /Kids [3 0 R] /Count 1 >>"),
		rawObject("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << >> /Contents 4 0 R >>"),
		stream([]byte("\n")),
		nil,
		stream([]byte(preamble)),
		stream([]byte(config)),
		stream([]byte(template)),
		stream([]byte(datasets)),
		stream([]byte(postamble)),
	}
	if foreground {
		objects[0] = rawObject("<< /Type /Catalog /Pages 2 0 R /AcroForm 5 0 R >>")
		objects[3] = stream([]byte("BT /F1 12 Tf 72 720 Td (MINTCLAW_XFA_FOREGROUND) Tj ET\n"))
	}
	if signed {
		fields = "[11 0 R]"
		annots := "/Annots [11 0 R] "
		permissions := "/Perms << /DocMDP 12 0 R >> "
		objects[0] = rawObject(
			"<< /Type /Catalog /Pages 2 0 R /NeedsRendering true /AcroForm 5 0 R " + permissions + ">>",
		)
		objects[2] = rawObject(
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 612 792] /Resources << >> " +
				annots + "/Contents 4 0 R >>",
		)
		objects = append(objects,
			rawObject("<< /Type /Annot /Subtype /Widget /FT /Sig /T (approval) /V 12 0 R /Rect [0 0 0 0] /P 3 0 R >>"),
			rawObject("<< /Type /Sig /Filter /Adobe.PPKLite /SubFilter /adbe.pkcs7.detached "+
				"/ByteRange [0 0 0 0] /Contents <00> /Reference [<< /TransformMethod /DocMDP "+
				"/TransformParams << /Type /TransformParams /P 2 /V /1.2 >> >>] >>"),
		)
	}
	objects[4] = rawObject(fmt.Sprintf(
		"<< /Fields %s /SigFlags %d /DR << /Font << >> >> /XFA [(xdp:xdp) 6 0 R (config) 7 0 R "+
			"(template) 8 0 R (datasets) 9 0 R (/xdp:xdp) 10 0 R] >>",
		fields,
		boolInt(signed)*3,
	))
	return fixture{name: name, objects: objects}
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func rawObject(value string) pdfObject {
	return pdfObject(value)
}

func stream(data []byte) pdfObject {
	return pdfObject(fmt.Sprintf("<< /Length %d >>\nstream\n", len(data)) + string(data) + "\nendstream")
}

func encodePDF(objects []pdfObject) []byte {
	var output bytes.Buffer
	output.WriteString("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n")
	offsets := make([]int, len(objects)+1)
	for index, value := range objects {
		offsets[index+1] = output.Len()
		_, _ = fmt.Fprintf(&output, "%d 0 obj\n", index+1)
		_, _ = output.Write(value)
		output.WriteString("\nendobj\n")
	}
	xref := output.Len()
	_, _ = fmt.Fprintf(&output, "xref\n0 %d\n", len(offsets))
	output.WriteString("0000000000 65535 f \n")
	for _, offset := range offsets[1:] {
		_, _ = fmt.Fprintf(&output, "%010d 00000 n \n", offset)
	}
	_, _ = fmt.Fprintf(
		&output,
		"trailer\n<< /Size %d /Root 1 0 R /ID [<4d696e74436c61775044463441><4d696e74436c61775044463441>] >>\n"+
			"startxref\n%d\n%%%%EOF\n",
		len(offsets),
		xref,
	)
	return output.Bytes()
}

func fatalf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
