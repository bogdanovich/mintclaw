package main

import (
	"bytes"
	"compress/zlib"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type fixture struct {
	name    string
	objects []pdfObject
	data    []byte
}

type manifest struct {
	SchemaVersion string            `json:"schema_version"`
	Privacy       string            `json:"privacy"`
	Generator     string            `json:"generator"`
	License       string            `json:"license"`
	Fixtures      []manifestFixture `json:"fixtures"`
}

type manifestFixture struct {
	ID           string                 `json:"id"`
	File         string                 `json:"file"`
	SHA256       string                 `json:"sha256"`
	Construction string                 `json:"construction"`
	License      string                 `json:"license"`
	Expected     map[string]interface{} `json:"expected"`
	EvidenceTest string                 `json:"evidence_test"`
}

func main() {
	root, err := filepath.Abs(filepath.Join("pkg", "document", "testdata"))
	must(err)
	fixtures := []fixture{
		textFixture(),
		imageOnlyFixture(),
		mixedFixture(),
		acroFormFixture(),
		xfaFixture("xfa-dynamic.pdf", false, "required"),
		xfaFixture("hybrid-xfa-static.pdf", true, "forbidden"),
		xfaLimitFixture(),
		unsignedSignatureFixture(),
		signedFixture("signed-certified.pdf", "Sig", "DocMDP"),
		signedFixture("field-restricted.pdf", "Sig", "FieldMDP"),
		signedFixture("timestamped.pdf", "DocTimeStamp", ""),
		rightsEnabledFixture(),
		truncatedFixture(),
		malformedXRefFixture(),
		oversizedStreamDeclarationFixture(),
		oversizedObjectCountFixture(),
		decodedContentLimitFixture(),
		adversarialNestingFixture(),
	}
	result := manifest{
		SchemaVersion: "mintclaw.document_inspection_fixture_manifest.v1",
		Privacy:       "synthetic_only",
		Generator:     "testdata/generate/main.go",
		License:       "MIT (MintClaw repository)",
	}
	for _, item := range fixtures {
		data := item.data
		if data == nil {
			data = encodePDF(item.objects)
		}
		must(os.WriteFile(filepath.Join(root, item.name), data, 0o644))
		digest := sha256.Sum256(data)
		result.Fixtures = append(result.Fixtures, manifestEntry(item.name, hex.EncodeToString(digest[:])))
	}
	encrypted, err := os.ReadFile(filepath.Join(root, "encrypted-password-required.pdf"))
	must(err)
	encryptedDigest := sha256.Sum256(encrypted)
	result.Fixtures = append(result.Fixtures, manifestFixture{
		ID:           "encrypted-password-required",
		File:         "encrypted-password-required.pdf",
		SHA256:       hex.EncodeToString(encryptedDigest[:]),
		Construction: "pdfcpu_v0.15.0_aes256_one_time_password_discarded",
		License:      "MIT (MintClaw repository)",
		Expected: map[string]interface{}{
			"state":             "unsupported",
			"failure_code":      "password_required",
			"encryption":        "present",
			"password_required": "present",
		},
		EvidenceTest: "TestPDFCPUBackendMatchesInspectionManifest",
	})
	encoded, err := json.MarshalIndent(result, "", "  ")
	must(err)
	encoded = append(encoded, '\n')
	must(os.WriteFile(filepath.Join(root, "inspection-manifest.json"), encoded, 0o644))
}

func textFixture() fixture {
	return fixture{name: "text.pdf", objects: []pdfObject{
		catalog("2 0 R", ""),
		pages("3 0 R"),
		page("2 0 R", "5 0 R", "/Font << /F1 4 0 R >>", ""),
		rawObject("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"),
		stream("BT /F1 12 Tf 72 720 Td (MintClaw text fixture) Tj ET\n"),
	}}
}

func imageOnlyFixture() fixture {
	return fixture{name: "image-only.pdf", objects: []pdfObject{
		catalog("2 0 R", ""),
		pages("3 0 R"),
		page("2 0 R", "5 0 R", "/XObject << /Im1 4 0 R >>", ""),
		streamDict("/Type /XObject /Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceRGB /BitsPerComponent 8", []byte{0, 0, 0}),
		stream("q 10 0 0 10 72 720 cm /Im1 Do Q\n"),
	}}
}

func mixedFixture() fixture {
	return fixture{name: "mixed-pages.pdf", objects: []pdfObject{
		catalog("2 0 R", ""),
		pages("3 0 R 4 0 R"),
		page("2 0 R", "7 0 R", "/Font << /F1 5 0 R >>", ""),
		page("2 0 R", "8 0 R", "/XObject << /Im1 6 0 R >>", ""),
		rawObject("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"),
		streamDict("/Type /XObject /Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceRGB /BitsPerComponent 8", []byte{0, 0, 0}),
		stream("BT /F1 12 Tf 72 720 Td (MintClaw mixed text page) Tj ET\n"),
		stream("q 10 0 0 10 72 720 cm /Im1 Do Q\n"),
	}}
}

func acroFormFixture() fixture {
	return fixture{name: "acroform.pdf", objects: []pdfObject{
		catalog("2 0 R", "/AcroForm 6 0 R"),
		pages("3 0 R"),
		page("2 0 R", "5 0 R", "/Font << /F1 4 0 R >>", "/Annots [7 0 R]"),
		rawObject("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"),
		stream("BT /F1 12 Tf 72 720 Td (Synthetic AcroForm) Tj ET\n"),
		rawObject("<< /Fields [7 0 R] /NeedAppearances true >>"),
		rawObject("<< /Type /Annot /Subtype /Widget /FT /Tx /T (name) /V (MintClaw) /DA (/F1 12 Tf 0 g) /Rect [72 650 250 675] /P 3 0 R >>"),
	}}
}

func xfaFixture(name string, hybrid bool, dynamicRender string) fixture {
	fields := "[]"
	annots := ""
	objects := []pdfObject{
		catalog("2 0 R", "/AcroForm 6 0 R"),
		pages("3 0 R"),
		page("2 0 R", "5 0 R", "/Font << /F1 4 0 R >>", annots),
		rawObject("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"),
		stream("BT /F1 12 Tf 72 720 Td (Synthetic XFA fixture) Tj ET\n"),
	}
	if hybrid {
		fields = "[8 0 R]"
		annots = "/Annots [8 0 R]"
		objects[2] = page("2 0 R", "5 0 R", "/Font << /F1 4 0 R >>", annots)
	}
	objects = append(objects,
		rawObject(fmt.Sprintf("<< /Fields %s /XFA 7 0 R >>", fields)),
		stream(fmt.Sprintf(
			`<?xml version="1.0"?><xdp:xdp xmlns:xdp="http://ns.adobe.com/xdp/"><config><present><pdf><dynamicRender>%s</dynamicRender></pdf></present></config></xdp:xdp>`,
			dynamicRender,
		)),
	)
	if hybrid {
		objects = append(objects, rawObject(
			"<< /Type /Annot /Subtype /Widget /FT /Tx /T (hybrid-name) /DA (/F1 12 Tf 0 g) /Rect [72 650 250 675] /P 3 0 R >>",
		))
	}
	return fixture{name: name, objects: objects}
}

func xfaLimitFixture() fixture {
	payload := bytes.Repeat([]byte("X"), 2*1024*1024)
	return fixture{name: "xfa-decoded-limit.pdf", objects: []pdfObject{
		catalog("2 0 R", "/AcroForm 6 0 R"),
		pages("3 0 R"),
		page("2 0 R", "5 0 R", "/Font << /F1 4 0 R >>", ""),
		rawObject("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"),
		stream("BT /F1 12 Tf 72 720 Td (Synthetic XFA limit fixture) Tj ET\n"),
		rawObject("<< /Fields [] /XFA 7 0 R >>"),
		flateStream(payload),
	}}
}

func unsignedSignatureFixture() fixture {
	return fixture{name: "unsigned-signature.pdf", objects: []pdfObject{
		catalog("2 0 R", "/AcroForm 6 0 R"),
		pages("3 0 R"),
		page("2 0 R", "5 0 R", "/Font << /F1 4 0 R >>", "/Annots [7 0 R]"),
		rawObject("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"),
		stream("BT /F1 12 Tf 72 720 Td (Unsigned signature field) Tj ET\n"),
		rawObject("<< /SigFlags 3 /Fields [7 0 R] >>"),
		rawObject("<< /Type /Annot /Subtype /Widget /FT /Sig /T (approval) /Rect [72 650 250 675] /P 3 0 R >>"),
	}}
}

func signedFixture(name, signatureType, transform string) fixture {
	permissions := ""
	reference := ""
	if transform == "DocMDP" {
		permissions = "/Perms << /DocMDP 8 0 R >>"
		reference = "/Reference [<< /TransformMethod /DocMDP /TransformParams << /Type /TransformParams /P 2 /V /1.2 >> >>]"
	} else if transform == "FieldMDP" {
		reference = "/Reference [<< /TransformMethod /FieldMDP /TransformParams << /Type /TransformParams /Action /Include /Fields [(name)] /V /1.2 >> >>]"
	}
	subFilter := "adbe.pkcs7.detached"
	if signatureType == "DocTimeStamp" {
		subFilter = "ETSI.RFC3161"
	}
	return fixture{name: name, objects: []pdfObject{
		catalog("2 0 R", "/AcroForm 6 0 R "+permissions),
		pages("3 0 R"),
		page("2 0 R", "5 0 R", "/Font << /F1 4 0 R >>", "/Annots [7 0 R]"),
		rawObject("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"),
		stream("BT /F1 12 Tf 72 720 Td (Synthetic signed fixture) Tj ET\n"),
		rawObject("<< /SigFlags 3 /Fields [7 0 R] >>"),
		rawObject("<< /Type /Annot /Subtype /Widget /FT /Sig /T (approval) /V 8 0 R /Rect [72 650 250 675] /P 3 0 R >>"),
		rawObject(fmt.Sprintf(
			"<< /Type /%s /Filter /Adobe.PPKLite /SubFilter /%s /ByteRange [0 0 0 0] /Contents <00> %s >>",
			signatureType,
			subFilter,
			reference,
		)),
	}}
}

func rightsEnabledFixture() fixture {
	return fixture{name: "rights-enabled.pdf", objects: []pdfObject{
		catalog("2 0 R", "/Perms << /UR3 6 0 R >>"),
		pages("3 0 R"),
		page("2 0 R", "5 0 R", "/Font << /F1 4 0 R >>", ""),
		rawObject("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"),
		stream("BT /F1 12 Tf 72 720 Td (Synthetic usage rights fixture) Tj ET\n"),
		rawObject("<< /Type /Sig /Filter /Adobe.PPKLite /SubFilter /adbe.pkcs7.detached /ByteRange [0 0 0 0] /Contents <00> /Reference [<< /TransformMethod /UR3 /TransformParams << /Type /TransformParams /V /2.2 /Form true >> >>] >>"),
	}}
}

func truncatedFixture() fixture {
	data := encodePDF(textFixture().objects)
	return fixture{name: "truncated.pdf", data: data[:64]}
}

func malformedXRefFixture() fixture {
	data := encodePDF(textFixture().objects)
	data = bytes.Replace(data, []byte("0000000015 00000 n"), []byte("9999999999 00000 n"), 1)
	return fixture{name: "malformed-xref.pdf", data: data}
}

func oversizedStreamDeclarationFixture() fixture {
	return fixture{name: "oversized-stream-declaration.pdf", objects: []pdfObject{
		catalog("2 0 R", ""),
		pages("3 0 R"),
		page("2 0 R", "5 0 R", "/Font << /F1 4 0 R >>", ""),
		rawObject("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"),
		rawObject("<< /Length 999999999 >>\nstream\nBT (short) Tj ET\nendstream"),
	}}
}

func oversizedObjectCountFixture() fixture {
	data := encodePDF(textFixture().objects)
	data = bytes.Replace(data, []byte("xref\n0 6\n"), []byte("xref\n0 999999999\n"), 1)
	return fixture{name: "oversized-object-count.pdf", data: data}
}

func decodedContentLimitFixture() fixture {
	decoded := bytes.Repeat([]byte("A"), 9*1024*1024)
	return fixture{name: "decoded-content-limit.pdf", objects: []pdfObject{
		catalog("2 0 R", ""),
		pages("3 0 R"),
		page("2 0 R", "5 0 R", "/Font << /F1 4 0 R >>", ""),
		rawObject("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"),
		flateStream(decoded),
	}}
}

func adversarialNestingFixture() fixture {
	nested := strings.Repeat("<< /N ", 256) + "0" + strings.Repeat(" >>", 256)
	return fixture{name: "adversarial-nesting.pdf", objects: []pdfObject{
		catalog("2 0 R", "/Adversarial "+nested),
		pages("3 0 R"),
		page("2 0 R", "5 0 R", "/Font << /F1 4 0 R >>", ""),
		rawObject("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"),
		stream("BT /F1 12 Tf 72 720 Td (Nested object fixture) Tj ET\n"),
	}}
}

func manifestEntry(name, digest string) manifestFixture {
	expected := map[string]interface{}{
		"state":        "succeeded",
		"pdf_version":  "1.7",
		"page_count":   1,
		"text":         "present",
		"acroform":     "absent",
		"xfa":          "absent",
		"signatures":   "absent",
		"restrictions": "absent",
	}
	switch name {
	case "image-only.pdf":
		expected["text"] = "absent"
	case "mixed-pages.pdf":
		expected["page_count"] = 2
		expected["text"] = "mixed"
	case "acroform.pdf":
		expected["acroform"] = "present"
		expected["field_count"] = 1
	case "xfa-dynamic.pdf":
		expected["acroform"] = "present"
		expected["field_count"] = 0
		expected["xfa"] = "present"
		expected["xfa_representation"] = "stream"
		expected["xfa_rendering"] = "dynamic"
	case "hybrid-xfa-static.pdf":
		expected["acroform"] = "present"
		expected["field_count"] = 1
		expected["xfa"] = "present"
		expected["xfa_representation"] = "stream"
		expected["xfa_rendering"] = "static"
	case "unsigned-signature.pdf":
		expected["acroform"] = "present"
		expected["field_count"] = 1
	case "signed-certified.pdf":
		expected["acroform"] = "present"
		expected["field_count"] = 1
		expected["signatures"] = "present"
		expected["signature_count"] = 1
		expected["certified"] = "present"
		expected["timestamped"] = "unknown"
		expected["restrictions"] = "present"
		expected["doc_mdp"] = "present"
	case "timestamped.pdf":
		expected["acroform"] = "present"
		expected["field_count"] = 1
		expected["signatures"] = "present"
		expected["signature_count"] = 1
		expected["certified"] = "absent"
		expected["timestamped"] = "present"
	case "field-restricted.pdf":
		expected["acroform"] = "present"
		expected["field_count"] = 1
		expected["signatures"] = "present"
		expected["signature_count"] = 1
		expected["certified"] = "absent"
		expected["timestamped"] = "unknown"
		expected["restrictions"] = "present"
		expected["field_mdp"] = "present"
	case "rights-enabled.pdf":
		expected["signatures"] = "present"
		expected["signature_count"] = 1
		expected["certified"] = "absent"
		expected["timestamped"] = "unknown"
		expected["restrictions"] = "present"
		expected["usage_rights"] = "present"
	case "truncated.pdf", "malformed-xref.pdf", "oversized-stream-declaration.pdf":
		expected = map[string]interface{}{
			"state":        "failed",
			"failure_code": "malformed_pdf",
		}
	case "oversized-object-count.pdf":
		expected = map[string]interface{}{
			"state":        "failed",
			"failure_code": "malformed_pdf",
		}
	case "decoded-content-limit.pdf":
		expected = map[string]interface{}{
			"state":        "failed",
			"failure_code": "inspection_limit",
		}
	case "xfa-decoded-limit.pdf":
		expected = map[string]interface{}{
			"state":        "failed",
			"failure_code": "inspection_limit",
		}
	case "adversarial-nesting.pdf":
		expected = map[string]interface{}{
			"state":        "failed",
			"failure_code": "malformed_pdf",
		}
	}
	return manifestFixture{
		ID:           name[:len(name)-len(filepath.Ext(name))],
		File:         name,
		SHA256:       digest,
		Construction: "deterministic_go_generator",
		License:      "MIT (MintClaw repository)",
		Expected:     expected,
		EvidenceTest: "TestPDFCPUBackendMatchesInspectionManifest",
	}
}

type pdfObject []byte

func catalog(pagesRef, extra string) pdfObject {
	return pdfObject(fmt.Sprintf("<< /Type /Catalog /Pages %s %s >>", pagesRef, extra))
}

func pages(kids string) pdfObject {
	count := 1
	if kids == "3 0 R 4 0 R" {
		count = 2
	}
	return pdfObject(fmt.Sprintf("<< /Type /Pages /Kids [%s] /Count %d >>", kids, count))
}

func page(parent, contents, resources, extra string) pdfObject {
	return pdfObject(fmt.Sprintf(
		"<< /Type /Page /Parent %s /MediaBox [0 0 612 792] /Resources << %s >> /Contents %s %s >>",
		parent,
		resources,
		contents,
		extra,
	))
}

func rawObject(value string) pdfObject {
	return pdfObject(value)
}

func stream(value string) pdfObject {
	return streamDict("", []byte(value))
}

func streamDict(dictionary string, data []byte) pdfObject {
	return pdfObject(fmt.Sprintf("<< %s /Length %d >>\nstream\n%s\nendstream", dictionary, len(data), data))
}

func flateStream(data []byte) pdfObject {
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	_, err := writer.Write(data)
	must(err)
	must(writer.Close())
	return streamDict("/Filter /FlateDecode", compressed.Bytes())
}

func encodePDF(objects []pdfObject) []byte {
	var output bytes.Buffer
	_, _ = output.WriteString("%PDF-1.7\n%\xe2\xe3\xcf\xd3\n")
	offsets := make([]int, len(objects)+1)
	for index, value := range objects {
		offsets[index+1] = output.Len()
		_, _ = fmt.Fprintf(&output, "%d 0 obj\n", index+1)
		_, _ = output.Write(value)
		_, _ = output.WriteString("\nendobj\n")
	}
	xref := output.Len()
	_, _ = fmt.Fprintf(&output, "xref\n0 %d\n", len(offsets))
	_, _ = output.WriteString("0000000000 65535 f \n")
	for _, offset := range offsets[1:] {
		_, _ = fmt.Fprintf(&output, "%010d 00000 n \n", offset)
	}
	_, _ = fmt.Fprintf(
		&output,
		"trailer\n<< /Size %d /Root 1 0 R /ID [<4d696e74436c61775044463042><4d696e74436c61775044463042>] >>\nstartxref\n%d\n%%%%EOF\n",
		len(offsets),
		xref,
	)
	return output.Bytes()
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
