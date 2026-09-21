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
	ID           string         `json:"id"`
	File         string         `json:"file"`
	SHA256       string         `json:"sha256"`
	Construction string         `json:"construction"`
	License      string         `json:"license"`
	Expected     map[string]any `json:"expected"`
	EvidenceTest string         `json:"evidence_test"`
}

func main() {
	root, err := filepath.Abs(filepath.Join("pkg", "document", "testdata"))
	must(err)
	fixtures := []fixture{
		textFixture(),
		imageOnlyFixture(),
		nameOperandsFixture(),
		inlineImageFixture(),
		mixedFixture(),
		acroFormFixture(),
		acroFormFieldsFixture(),
		calculatedFieldFixture(),
		xfaFixture("xfa-dynamic.pdf", false, "required"),
		xfaFixture("hybrid-xfa-static.pdf", true, "forbidden"),
		hybridXFAPacketArrayFixture(),
		xfaFixture("hybrid-xfa-dynamic.pdf", true, "required"),
		hybridXFACustomFixture(
			"hybrid-xfa-page-growth.pdf",
			`<?xml version="1.0"?><xdp:xdp xmlns:xdp="http://ns.adobe.com/xdp/"><config><present><pdf><dynamicRender>forbidden</dynamicRender></pdf></present></config><template><subform><occur max="2"/><overflow/></subform></template></xdp:xdp>`,
		),
		hybridXFACustomFixture(
			"hybrid-xfa-malformed.pdf",
			`<?xml version="1.0"?><xdp:xdp xmlns:xdp="http://ns.adobe.com/xdp/"><config><present><pdf><dynamicRender>required</dynamicRender></pdf></present></config><template><script/>`,
		),
		xfaLimitFixture(),
		metadataLimitFixture(),
		malformedMetadataFixture(),
		unsignedSignatureFixture(),
		signedFixture("signed-certified.pdf", "Sig", "DocMDP"),
		signedFixture("field-restricted.pdf", "Sig", "FieldMDP"),
		signedFixture("timestamped.pdf", "DocTimeStamp", ""),
		rightsEnabledFixture(),
		orphanStructureFixture(),
		truncatedFixture(),
		malformedXRefFixture(),
		oversizedStreamDeclarationFixture(),
		oversizedObjectCountFixture(),
		decodedContentLimitFixture(),
		decodedContentArrayLimitFixture(),
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
		Expected: map[string]any{
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
	writeReadFixtures(root)
	writeFormFieldsManifest(root)
}

func writeReadFixtures(root string) {
	fixtures := []fixture{
		unicodeFixture(),
		rotatedCropFixture(),
		ambiguousReadingOrderFixture(),
		extremeDimensionsFixture(),
		excessiveTextFixture(),
		manyPagesFixture(21),
	}
	for _, item := range fixtures {
		data := item.data
		if data == nil {
			data = encodePDF(item.objects)
		}
		must(os.WriteFile(filepath.Join(root, item.name), data, 0o644))
	}
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
		streamDict(
			"/Type /XObject /Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceRGB /BitsPerComponent 8",
			[]byte{0, 0, 0},
		),
		stream("q 10 0 0 10 72 720 cm /Im1 Do Q\n"),
	}}
}

func nameOperandsFixture() fixture {
	return fixture{name: "name-operands-no-text.pdf", objects: []pdfObject{
		catalog("2 0 R", ""),
		pages("3 0 R"),
		page("2 0 R", "4 0 R", "", ""),
		stream("/BT /Tj BDC EMC\n"),
	}}
}

func inlineImageFixture() fixture {
	return fixture{name: "inline-image.pdf", objects: []pdfObject{
		catalog("2 0 R", ""),
		pages("3 0 R"),
		page("2 0 R", "4 0 R", "", ""),
		streamDict("", []byte("q BI /W 1 /H 1 /BPC 8 /CS /RGB ID \x00\x00\x00 EI Q\n")),
	}}
}

func mixedFixture() fixture {
	return fixture{name: "mixed-pages.pdf", objects: []pdfObject{
		catalog("2 0 R", ""),
		pages("3 0 R 4 0 R"),
		page("2 0 R", "7 0 R", "/Font << /F1 5 0 R >>", ""),
		page("2 0 R", "8 0 R", "/XObject << /Im1 6 0 R >>", ""),
		rawObject("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"),
		streamDict(
			"/Type /XObject /Subtype /Image /Width 1 /Height 1 /ColorSpace /DeviceRGB /BitsPerComponent 8",
			[]byte{0, 0, 0},
		),
		stream("BT /F1 12 Tf 72 720 Td (MintClaw mixed text page) Tj ET\n"),
		stream("q 10 0 0 10 72 720 cm /Im1 Do Q\n"),
	}}
}

func unicodeFixture() fixture {
	return fixture{name: "unicode.pdf", objects: []pdfObject{
		catalog("2 0 R", ""),
		pages("3 0 R"),
		page("2 0 R", "5 0 R", "/Font << /F1 4 0 R >>", ""),
		rawObject("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>"),
		stream(`BT /F1 12 Tf 72 720 Td (MintClaw Caf\351 r\351sum\351) Tj ET` + "\n"),
	}}
}

func rotatedCropFixture() fixture {
	return fixture{name: "rotated-crop.pdf", objects: []pdfObject{
		catalog("2 0 R", ""),
		pages("3 0 R"),
		page(
			"2 0 R",
			"5 0 R",
			"/Font << /F1 4 0 R >>",
			"/Rotate 90 /CropBox [0 0 306 396]",
		),
		rawObject("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"),
		stream("BT /F1 12 Tf 72 300 Td (MINTCLAW_ROTATED_CROP) Tj ET\n"),
	}}
}

func ambiguousReadingOrderFixture() fixture {
	return fixture{name: "ambiguous-reading-order.pdf", objects: []pdfObject{
		catalog("2 0 R", ""),
		pages("3 0 R"),
		page("2 0 R", "5 0 R", "/Font << /F1 4 0 R >>", ""),
		rawObject("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"),
		stream(
			"BT /F1 12 Tf 72 120 Td (MINTCLAW_SECOND_VISUAL) Tj ET\n" +
				"BT /F1 12 Tf 72 720 Td (MINTCLAW_FIRST_VISUAL) Tj ET\n",
		),
	}}
}

func extremeDimensionsFixture() fixture {
	size := "1" + strings.Repeat("0", 90)
	return fixture{name: "extreme-dimensions.pdf", objects: []pdfObject{
		catalog("2 0 R", ""),
		pages("3 0 R"),
		rawObject(fmt.Sprintf(
			"<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %s %s] "+
				"/Resources << /Font << /F1 4 0 R >> >> /Contents 5 0 R >>",
			size,
			size,
		)),
		rawObject("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"),
		stream("BT /F1 12 Tf 72 720 Td (MINTCLAW_EXTREME_DIMENSIONS) Tj ET\n"),
	}}
}

func excessiveTextFixture() fixture {
	text := strings.Repeat("MINTCLAW_EXCESSIVE_TEXT_", 14_000)
	return fixture{name: "excessive-text.pdf", objects: []pdfObject{
		catalog("2 0 R", ""),
		pages("3 0 R"),
		page("2 0 R", "5 0 R", "/Font << /F1 4 0 R >>", ""),
		rawObject("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"),
		stream("BT /F1 8 Tf 20 720 Td (" + text + ") Tj ET\n"),
	}}
}

func manyPagesFixture(count int) fixture {
	objects := []pdfObject{catalog("2 0 R", "")}
	kids := make([]string, 0, count)
	for pageNumber := 1; pageNumber <= count; pageNumber++ {
		kids = append(kids, fmt.Sprintf("%d 0 R", pageNumber+2))
	}
	objects = append(objects, rawObject(fmt.Sprintf(
		"<< /Type /Pages /Kids [%s] /Count %d >>",
		strings.Join(kids, " "),
		count,
	)))
	fontObject := count + 3
	contentStart := fontObject + 1
	for pageNumber := 1; pageNumber <= count; pageNumber++ {
		objects = append(objects, page(
			"2 0 R",
			fmt.Sprintf("%d 0 R", contentStart+pageNumber-1),
			fmt.Sprintf("/Font << /F1 %d 0 R >>", fontObject),
			"",
		))
	}
	objects = append(objects, rawObject("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"))
	for pageNumber := 1; pageNumber <= count; pageNumber++ {
		objects = append(objects, stream(fmt.Sprintf(
			"BT /F1 12 Tf 72 720 Td (MINTCLAW_PAGE_%02d) Tj ET\n",
			pageNumber,
		)))
	}
	return fixture{name: "many-pages.pdf", objects: objects}
}

func acroFormFixture() fixture {
	return fixture{name: "acroform.pdf", objects: []pdfObject{
		catalog("2 0 R", "/AcroForm 6 0 R"),
		pages("3 0 R"),
		page("2 0 R", "5 0 R", "/Font << /F1 4 0 R >>", "/Annots [7 0 R]"),
		rawObject("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"),
		stream("BT /F1 12 Tf 72 720 Td (Synthetic AcroForm) Tj ET\n"),
		rawObject("<< /Fields [7 0 R] /NeedAppearances true >>"),
		rawObject(
			"<< /Type /Annot /Subtype /Widget /FT /Tx /T (name) /V (MintClaw) /DA (/F1 12 Tf 0 g) /Rect [72 650 250 675] /P 3 0 R >>",
		),
	}}
}

func acroFormFieldsFixture() fixture {
	return fixture{name: "acroform-fields.pdf", objects: []pdfObject{
		catalog("2 0 R", "/AcroForm 8 0 R"),
		pages("3 0 R 4 0 R"),
		page(
			"2 0 R",
			"6 0 R",
			"/Font << /F1 5 0 R >>",
			"/Annots [9 0 R 10 0 R 11 0 R 13 0 R 14 0 R 15 0 R 16 0 R 17 0 R 19 0 R]",
		),
		page("2 0 R", "7 0 R", "/Font << /F1 5 0 R >>", "/Annots [20 0 R]"),
		rawObject("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"),
		stream("BT /F1 12 Tf 72 750 Td (MINTCLAW_ACROFORM_FIELDS_PAGE_1) Tj ET\n"),
		stream("BT /F1 12 Tf 72 750 Td (MINTCLAW_ACROFORM_FIELDS_PAGE_2) Tj ET\n"),
		rawObject(
			"<< /Fields [9 0 R 10 0 R 11 0 R 12 0 R 15 0 R 16 0 R 25 0 R 18 0 R] " +
				"/NeedAppearances true /DR << /Font << /F1 5 0 R >> >> /DA (/F1 12 Tf 0 g) >>",
		),
		rawObject(
			"<< /Type /Annot /Subtype /Widget /FT /Tx /T (full_name) /TU (Full name) /Ff 2 " +
				"/MaxLen 80 /V (Existing User) /DA (/F1 12 Tf 0 g) /Rect [72 680 300 704] /P 3 0 R >>",
		),
		rawObject(
			"<< /Type /Annot /Subtype /Widget /FT /Tx /T (notes) /TU (Notes) /Ff 4096 " +
				"/DA (/F1 12 Tf 0 g) /Rect [72 620 300 670] /P 3 0 R >>",
		),
		rawObject(
			"<< /Type /Annot /Subtype /Widget /FT /Btn /T (agree) /TU (Agree) /V /Yes /AS /Yes " +
				"/AP << /N << /Off 21 0 R /Yes 22 0 R >> >> /Rect [72 580 88 596] /P 3 0 R >>",
		),
		rawObject("<< /FT /Btn /T (color) /TU (Color) /Ff 32768 /Kids [13 0 R 14 0 R] /V /Red >>"),
		rawObject(
			"<< /Type /Annot /Subtype /Widget /Parent 12 0 R /AS /Red " +
				"/AP << /N << /Off 21 0 R /Red 23 0 R >> >> /Rect [72 540 88 556] /P 3 0 R >>",
		),
		rawObject(
			"<< /Type /Annot /Subtype /Widget /Parent 12 0 R /AS /Off " +
				"/AP << /N << /Off 21 0 R /Blue 24 0 R >> >> /Rect [108 540 124 556] /P 3 0 R >>",
		),
		rawObject(
			"<< /Type /Annot /Subtype /Widget /FT /Ch /T (country) /TU (Country) /Ff 131072 " +
				"/Opt [[(US) (United States)] [(CA) (Canada)] [( EU ) ( Europe )]] " +
				"/V (US) /DA (/F1 12 Tf 0 g) " +
				"/Rect [72 490 260 516] /P 3 0 R >>",
		),
		rawObject(
			"<< /Type /Annot /Subtype /Widget /FT /Ch /T (tags) /TU (Tags) /Ff 2097152 " +
				"/Opt [(one) (two) (three)] /V [(one) (three)] /DA (/F1 12 Tf 0 g) " +
				"/Rect [72 410 260 480] /P 3 0 R >>",
		),
		rawObject(
			"<< /Type /Annot /Subtype /Widget /Parent 25 0 R /FT /Tx /TU (Start date) " +
				"/AA << /F << /S /JavaScript /JS (AFDate_FormatEx\\(\"mm/dd/yyyy\"\\)) >> >> " +
				"/DA (/F1 12 Tf 0 g) /Rect [72 360 260 386] /P 3 0 R >>",
		),
		rawObject(
			"<< /FT /Tx /T (repeated) /TU (Repeated value) /Kids [19 0 R 20 0 R] " +
				"/V (Same value) /DA (/F1 12 Tf 0 g) >>",
		),
		rawObject("<< /Type /Annot /Subtype /Widget /Parent 18 0 R /Rect [72 310 260 336] /P 3 0 R >>"),
		rawObject("<< /Type /Annot /Subtype /Widget /Parent 18 0 R /Rect [72 680 260 706] /P 4 0 R >>"),
		formAppearance(""),
		formAppearance("0 0 1 rg 1 1 14 14 re f"),
		formAppearance("1 0 0 rg 1 1 14 14 re f"),
		formAppearance("0 0 1 rg 1 1 14 14 re f"),
		rawObject("<< /T (start_date) /MaxLen 10 /Kids [17 0 R] >>"),
	}}
}

func formAppearance(content string) pdfObject {
	return streamDict("/Type /XObject /Subtype /Form /BBox [0 0 16 16]", []byte(content))
}

func calculatedFieldFixture() fixture {
	return fixture{name: "calculated-field.pdf", objects: []pdfObject{
		catalog("2 0 R", "/AcroForm 6 0 R"),
		pages("3 0 R"),
		page("2 0 R", "5 0 R", "/Font << /F1 4 0 R >>", "/Annots [7 0 R]"),
		rawObject("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"),
		stream("BT /F1 12 Tf 72 720 Td (Calculated field refusal) Tj ET\n"),
		rawObject("<< /Fields [7 0 R] /DR << /Font << /F1 4 0 R >> >> /DA (/F1 12 Tf 0 g) >>"),
		rawObject(
			"<< /Type /Annot /Subtype /Widget /FT /Tx /T (calculated) " +
				"/AA << /C << /S /JavaScript /JS (event.value = 1) >> >> " +
				"/DA (/F1 12 Tf 0 g) /Rect [72 650 250 675] /P 3 0 R >>",
		),
	}}
}

func writeFormFieldsManifest(root string) {
	type fieldsFixture struct {
		ID           string         `json:"id"`
		File         string         `json:"file"`
		SHA256       string         `json:"sha256"`
		Construction string         `json:"construction"`
		License      string         `json:"license"`
		Expected     map[string]any `json:"expected"`
		EvidenceTest string         `json:"evidence_test"`
	}
	type fieldsManifest struct {
		SchemaVersion string          `json:"schema_version"`
		Privacy       string          `json:"privacy"`
		Generator     string          `json:"generator"`
		License       string          `json:"license"`
		Fixtures      []fieldsFixture `json:"fixtures"`
	}
	entries := []struct {
		id       string
		file     string
		expected map[string]any
	}{
		{id: "supported-field-matrix", file: "acroform-fields.pdf", expected: map[string]any{
			"state": "succeeded", "field_count": 8,
			"kinds":        []string{"checkbox", "combo", "date", "list", "radio", "text"},
			"widget_count": 10,
		}},
		{id: "form-not-present", file: "text.pdf", expected: map[string]any{
			"state": "unsupported", "failure_code": "form_not_present",
		}},
		{id: "hybrid-xfa-discovery", file: "hybrid-xfa-packet-array.pdf", expected: map[string]any{
			"state": "succeeded", "field_count": 1, "kinds": []string{"text"}, "widget_count": 1,
		}},
		{id: "hybrid-xfa-dynamic-refusal", file: "hybrid-xfa-dynamic.pdf", expected: map[string]any{
			"state": "unsupported", "failure_code": "form_unsupported",
		}},
		{id: "hybrid-xfa-page-growth-refusal", file: "hybrid-xfa-page-growth.pdf", expected: map[string]any{
			"state": "unsupported", "failure_code": "form_unsupported",
		}},
		{id: "hybrid-xfa-malformed-refusal", file: "hybrid-xfa-malformed.pdf", expected: map[string]any{
			"state": "unsupported", "failure_code": "form_unsupported",
		}},
		{id: "signed-refusal", file: "signed-certified.pdf", expected: map[string]any{
			"state": "unsupported", "failure_code": "form_unsupported",
		}},
		{id: "signature-field-refusal", file: "unsigned-signature.pdf", expected: map[string]any{
			"state": "unsupported", "failure_code": "field_unsupported",
		}},
		{id: "calculated-field-refusal", file: "calculated-field.pdf", expected: map[string]any{
			"state": "unsupported", "failure_code": "field_unsupported",
		}},
		{id: "password-refusal", file: "encrypted-password-required.pdf", expected: map[string]any{
			"state": "unsupported", "failure_code": "password_required",
		}},
	}
	result := fieldsManifest{
		SchemaVersion: "mintclaw.document_form_fields_fixture_manifest.v1",
		Privacy:       "synthetic_only",
		Generator:     "testdata/generate/main.go",
		License:       "MIT (MintClaw repository)",
	}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(root, entry.file))
		must(err)
		digest := sha256.Sum256(data)
		result.Fixtures = append(result.Fixtures, fieldsFixture{
			ID: entry.id, File: entry.file, SHA256: hex.EncodeToString(digest[:]),
			Construction: "deterministic_go_generator", License: "MIT (MintClaw repository)",
			Expected: entry.expected, EvidenceTest: "TestPDFCPUFormFieldsBackendMatchesManifest",
		})
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	must(err)
	encoded = append(encoded, '\n')
	must(os.WriteFile(filepath.Join(root, "form-fields-manifest.json"), encoded, 0o644))
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

func hybridXFAPacketArrayFixture() fixture {
	return fixture{name: "hybrid-xfa-packet-array.pdf", objects: []pdfObject{
		catalog("2 0 R", "/AcroForm 6 0 R"),
		pages("3 0 R"),
		page("2 0 R", "5 0 R", "/Font << /F1 4 0 R >>", "/Annots [10 0 R]"),
		rawObject("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"),
		stream("BT /F1 12 Tf 72 720 Td (Synthetic packet-array hybrid fixture) Tj ET\n"),
		rawObject(
			"<< /Fields [10 0 R] /XFA [(xdp:xdp) 7 0 R (config) 8 0 R (/xdp:xdp) 9 0 R] >>",
		),
		stream(`<?xml version="1.0"?><xdp:xdp xmlns:xdp="http://ns.adobe.com/xdp/">`),
		stream(`<config><present><pdf><dynamicRender>forbidden</dynamicRender></pdf></present>` +
			`<script contentType="application/x-javascript">1+1</script></config>`),
		stream(`</xdp:xdp>`),
		rawObject(
			"<< /Type /Annot /Subtype /Widget /FT /Tx /T (hybrid-name) " +
				"/DA (/F1 12 Tf 0 g) /Rect [72 650 250 675] /P 3 0 R >>",
		),
	}}
}

func hybridXFACustomFixture(name, payload string) fixture {
	return fixture{name: name, objects: []pdfObject{
		catalog("2 0 R", "/AcroForm 6 0 R"),
		pages("3 0 R"),
		page("2 0 R", "5 0 R", "/Font << /F1 4 0 R >>", "/Annots [8 0 R]"),
		rawObject("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"),
		stream("BT /F1 12 Tf 72 720 Td (Synthetic custom hybrid fixture) Tj ET\n"),
		rawObject("<< /Fields [8 0 R] /XFA 7 0 R >>"),
		stream(payload),
		rawObject(
			"<< /Type /Annot /Subtype /Widget /FT /Tx /T (hybrid-name) " +
				"/DA (/F1 12 Tf 0 g) /Rect [72 650 250 675] /P 3 0 R >>",
		),
	}}
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

func metadataLimitFixture() fixture {
	payload := append([]byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/">`), bytes.Repeat([]byte(" "), 9*1024*1024)...)
	payload = append(payload, []byte(`</x:xmpmeta>`)...)
	return fixture{name: "metadata-decoded-limit.pdf", objects: []pdfObject{
		catalog("2 0 R", "/Metadata 6 0 R"),
		pages("3 0 R"),
		page("2 0 R", "5 0 R", "/Font << /F1 4 0 R >>", ""),
		rawObject("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"),
		stream("BT /F1 12 Tf 72 720 Td (Synthetic metadata limit fixture) Tj ET\n"),
		flateStreamDict("/Type /Metadata /Subtype /XML", payload),
	}}
}

func malformedMetadataFixture() fixture {
	return fixture{name: "malformed-metadata.pdf", objects: []pdfObject{
		catalog("2 0 R", "/Metadata 6 0 R"),
		pages("3 0 R"),
		page("2 0 R", "5 0 R", "/Font << /F1 4 0 R >>", ""),
		rawObject("<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica >>"),
		stream("BT /F1 12 Tf 72 720 Td (Synthetic malformed metadata fixture) Tj ET\n"),
		flateStreamDict(
			"/Type /Metadata /Subtype /BAD",
			[]byte(`<x:xmpmeta xmlns:x="adobe:ns:meta/"/>`),
		),
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
	switch transform {
	case "DocMDP":
		permissions = "/Perms << /DocMDP 8 0 R >>"
		reference = "/Reference [<< /TransformMethod /DocMDP /TransformParams << /Type /TransformParams /P 2 /V /1.2 >> >>]"
	case "FieldMDP":
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
		rawObject(
			"<< /Type /Annot /Subtype /Widget /FT /Sig /T (approval) /V 8 0 R /Rect [72 650 250 675] /P 3 0 R >>",
		),
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
		rawObject(
			"<< /Type /Sig /Filter /Adobe.PPKLite /SubFilter /adbe.pkcs7.detached /ByteRange [0 0 0 0] /Contents <00> /Reference [<< /TransformMethod /UR3 /TransformParams << /Type /TransformParams /V /2.2 /Form true >> >>] >>",
		),
	}}
}

func orphanStructureFixture() fixture {
	return fixture{name: "orphan-structure.pdf", objects: []pdfObject{
		catalog("2 0 R", ""),
		pages("3 0 R"),
		page("2 0 R", "4 0 R", "", ""),
		stream("0 0 m 10 10 l s\n"),
		rawObject("<< /Fields [] /XFA 7 0 R >>"),
		rawObject("<< /Reference [<< /TransformMethod /FieldMDP >>] >>"),
		stream("<xdp:xdp><dynamicRender>required</dynamicRender></xdp:xdp>"),
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

func decodedContentArrayLimitFixture() fixture {
	decoded := bytes.Repeat([]byte("A"), 5*1024*1024)
	return fixture{name: "decoded-content-array-limit.pdf", objects: []pdfObject{
		catalog("2 0 R", ""),
		pages("3 0 R"),
		page("2 0 R", "[4 0 R 5 0 R]", "", ""),
		flateStream(decoded),
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
	expected := map[string]any{
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
	case "image-only.pdf", "name-operands-no-text.pdf", "orphan-structure.pdf":
		expected["text"] = "absent"
	case "inline-image.pdf":
		expected["text"] = "unknown"
	case "mixed-pages.pdf":
		expected["page_count"] = 2
		expected["text"] = "mixed"
	case "acroform.pdf":
		expected["acroform"] = "present"
		expected["field_count"] = 1
	case "acroform-fields.pdf":
		expected["acroform"] = "present"
		expected["field_count"] = 8
		expected["page_count"] = 2
	case "calculated-field.pdf":
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
	case "hybrid-xfa-packet-array.pdf":
		expected["acroform"] = "present"
		expected["field_count"] = 1
		expected["xfa"] = "present"
		expected["xfa_representation"] = "packet_array"
		expected["xfa_rendering"] = "static"
	case "hybrid-xfa-dynamic.pdf":
		expected["acroform"] = "present"
		expected["field_count"] = 1
		expected["xfa"] = "present"
		expected["xfa_representation"] = "stream"
		expected["xfa_rendering"] = "dynamic"
	case "hybrid-xfa-page-growth.pdf":
		expected["acroform"] = "present"
		expected["field_count"] = 1
		expected["xfa"] = "present"
		expected["xfa_representation"] = "stream"
		expected["xfa_rendering"] = "static"
	case "hybrid-xfa-malformed.pdf":
		expected["acroform"] = "present"
		expected["field_count"] = 1
		expected["xfa"] = "present"
		expected["xfa_representation"] = "stream"
	case "unsigned-signature.pdf":
		expected["acroform"] = "present"
		expected["field_count"] = 1
	case "signed-certified.pdf":
		expected["acroform"] = "present"
		expected["field_count"] = 1
		expected["signatures"] = "present"
		expected["signature_count"] = 1
		expected["certified"] = "present"
		expected["timestamped"] = "absent"
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
		expected["timestamped"] = "absent"
		expected["restrictions"] = "present"
		expected["field_mdp"] = "present"
	case "rights-enabled.pdf":
		expected["signatures"] = "present"
		expected["signature_count"] = 1
		expected["certified"] = "absent"
		expected["timestamped"] = "absent"
		expected["restrictions"] = "present"
		expected["usage_rights"] = "present"
	case "truncated.pdf", "malformed-xref.pdf", "oversized-stream-declaration.pdf", "malformed-metadata.pdf":
		expected = map[string]any{
			"state":        "failed",
			"failure_code": "malformed_pdf",
		}
	case "oversized-object-count.pdf":
		expected = map[string]any{
			"state":        "failed",
			"failure_code": "malformed_pdf",
		}
	case "decoded-content-limit.pdf", "decoded-content-array-limit.pdf", "metadata-decoded-limit.pdf":
		expected = map[string]any{
			"state":        "failed",
			"failure_code": "inspection_limit",
		}
	case "xfa-decoded-limit.pdf":
		expected = map[string]any{
			"state":        "failed",
			"failure_code": "inspection_limit",
		}
	case "adversarial-nesting.pdf":
		expected = map[string]any{
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
	return flateStreamDict("", data)
}

func flateStreamDict(dictionary string, data []byte) pdfObject {
	var compressed bytes.Buffer
	writer := zlib.NewWriter(&compressed)
	_, err := writer.Write(data)
	must(err)
	must(writer.Close())
	return streamDict(strings.TrimSpace(dictionary+" /Filter /FlateDecode"), compressed.Bytes())
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
