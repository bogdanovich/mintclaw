// Command gate applies the PDF4A qualification-only static-XFA admission gate.
//
// It is intentionally not a production parser. It accepts only the small,
// deterministic corpus emitted by ../generate and demonstrates the positive
// and negative boundaries that a later PDF4B implementation must enforce with
// the existing document inspection backend.
package main

import (
	"bytes"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
)

const (
	maxPacketBytes = 1 << 20
	maxXMLDepth    = 64
	maxXMLNodes    = 100_000
)

var packetRefPattern = regexp.MustCompile(`\(([^)]+)\)\s+(\d+)\s+0\s+R`)

type outcome struct {
	File      string `json:"file"`
	Decision  string `json:"decision"`
	Reason    string `json:"reason"`
	Rendering string `json:"rendering,omitempty"`
}

func main() {
	flag.Parse()
	if flag.NArg() == 0 {
		fatalf("usage: gate INPUT.pdf [INPUT.pdf ...]")
	}
	encoder := json.NewEncoder(os.Stdout)
	encoder.SetIndent("", "  ")
	for _, path := range flag.Args() {
		data, err := os.ReadFile(path)
		if err != nil {
			fatalf("read %s: %v", path, err)
		}
		result := qualify(path, data)
		if err := encoder.Encode(result); err != nil {
			fatalf("encode result: %v", err)
		}
	}
}

func qualify(path string, data []byte) outcome {
	reject := func(reason string) outcome {
		return outcome{File: path, Decision: "refuse", Reason: reason}
	}
	if bytes.Contains(data, []byte("/Encrypt")) {
		return reject("signed_restricted_or_encrypted")
	}
	if !bytes.Contains(data, []byte("/XFA [")) {
		return reject("xfa_packet_array_required")
	}
	if !bytes.Contains(data, []byte("/NeedsRendering true")) {
		return reject("foreground_or_rendering_authority_unknown")
	}
	for _, marker := range [][]byte{
		[]byte("/FT /Sig"), []byte("/DocMDP"), []byte("/FieldMDP"),
		[]byte("/UR3"), []byte("/Perms"),
	} {
		if bytes.Contains(data, marker) {
			return reject("signed_restricted_or_encrypted")
		}
	}
	if !bytes.Contains(data, []byte("/Fields []")) {
		return reject("hybrid_or_acroform_authority_conflict")
	}

	packets, err := extractPackets(data)
	if err != nil {
		return reject("malformed_packet_array")
	}
	for _, required := range []string{"config", "template", "datasets"} {
		if len(packets[required]) == 0 {
			return reject("required_packet_missing")
		}
		if len(packets[required]) > maxPacketBytes {
			return reject("packet_limit_exceeded")
		}
		if err := validateXML(packets[required]); err != nil {
			return reject("malformed_or_unsafe_xml")
		}
	}
	if !bytes.Contains(packets["config"], []byte("<dynamicRender>forbidden</dynamicRender>")) {
		return reject("dynamic_or_unknown_rendering")
	}

	template := strings.ToLower(string(packets["template"]))
	for _, token := range []string{
		"<script", "<event", "<submit", "<occur", "<break", "<overflow", "<connect",
		"formcalc", "javascript", "gotourl", "href=", "layout=\"flowed\"", "layout=\"tb\"",
	} {
		if strings.Contains(template, token) {
			if token == "<occur" || token == "<break" || token == "<overflow" || strings.HasPrefix(token, "layout=") {
				return reject("repeating_or_page_growth")
			}
			return reject("script_action_or_external_data")
		}
	}
	for name := range packets {
		switch name {
		case "xdp:xdp", "/xdp:xdp", "config", "template", "datasets":
		default:
			return reject("unsupported_xfa_packet")
		}
	}
	return outcome{
		File:      path,
		Decision:  "candidate",
		Reason:    "pure_static_script_free_unsigned_packet_array",
		Rendering: "static",
	}
}

func extractPackets(data []byte) (map[string][]byte, error) {
	acro := bytes.Index(data, []byte("/XFA ["))
	if acro < 0 {
		return nil, errors.New("XFA array missing")
	}
	end := bytes.IndexByte(data[acro:], ']')
	if end < 0 {
		return nil, errors.New("XFA array unterminated")
	}
	array := data[acro : acro+end+1]
	refs := packetRefPattern.FindAllSubmatch(array, -1)
	if len(refs) == 0 {
		return nil, errors.New("XFA packets missing")
	}
	packets := make(map[string][]byte, len(refs))
	for _, match := range refs {
		objectNumber, err := strconv.Atoi(string(match[2]))
		if err != nil {
			return nil, err
		}
		streamData, err := extractStream(data, objectNumber)
		if err != nil {
			return nil, err
		}
		packets[string(match[1])] = streamData
	}
	return packets, nil
}

func extractStream(data []byte, objectNumber int) ([]byte, error) {
	header := []byte(fmt.Sprintf("%d 0 obj", objectNumber))
	start := bytes.Index(data, header)
	if start < 0 {
		return nil, errors.New("referenced object missing")
	}
	streamStart := bytes.Index(data[start:], []byte("stream\n"))
	if streamStart < 0 {
		return nil, errors.New("referenced object is not a stream")
	}
	streamStart += start + len("stream\n")
	streamEnd := bytes.Index(data[streamStart:], []byte("\nendstream"))
	if streamEnd < 0 {
		return nil, errors.New("stream unterminated")
	}
	return data[streamStart : streamStart+streamEnd], nil
}

func validateXML(data []byte) error {
	lower := bytes.ToLower(data)
	if bytes.Contains(lower, []byte("<!doctype")) || bytes.Contains(lower, []byte("<!entity")) {
		return errors.New("DTD or entity declaration forbidden")
	}
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.Strict = true
	depth := 0
	nodes := 0
	for {
		token, err := decoder.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		nodes++
		if nodes > maxXMLNodes {
			return errors.New("XML node limit exceeded")
		}
		switch token.(type) {
		case xml.StartElement:
			depth++
			if depth > maxXMLDepth {
				return errors.New("XML depth limit exceeded")
			}
		case xml.EndElement:
			depth--
		}
	}
	if depth != 0 {
		return errors.New("XML depth mismatch")
	}
	return nil
}

func fatalf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
