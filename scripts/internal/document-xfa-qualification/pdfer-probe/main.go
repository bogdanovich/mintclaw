// Command pdfer-probe qualifies a pinned pdfer revision as an XFA packet
// updater. It is a disposable test program, not a MintClaw runtime dependency.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	pdfer "github.com/benedoc-inc/pdfer/v2"
	"github.com/benedoc-inc/pdfer/v2/forms/xfa"
)

type result struct {
	Mode              string `json:"mode"`
	SourceSHA256      string `json:"source_sha256"`
	SourceUnchanged   bool   `json:"source_unchanged"`
	OutputSHA256      string `json:"output_sha256"`
	OutputBytes       int    `json:"output_bytes"`
	DatasetWellFormed bool   `json:"dataset_well_formed"`
	VisibleValue      string `json:"expected_visible_value"`
}

func main() {
	mode := flag.String("mode", "safe", "mutation mode: safe or raw-negative-control")
	value := flag.String("value", "MINTCLAW_XFA_AFTER", "replacement value")
	flag.Parse()
	if flag.NArg() != 2 {
		fatalf("usage: pdfer-probe [--mode safe|raw-negative-control] [--value VALUE] INPUT.pdf OUTPUT.pdf")
	}
	if *mode != "safe" && *mode != "raw-negative-control" {
		fatalf("unsupported --mode %q", *mode)
	}
	inputPath, outputPath := flag.Arg(0), flag.Arg(1)
	source, err := os.ReadFile(inputPath)
	must(err)
	sourceDigest := digest(source)

	form, err := pdfer.ExtractForm(source, nil, false)
	must(err)
	replacement := *value
	if *mode == "safe" {
		var escaped bytes.Buffer
		must(xml.EscapeText(&escaped, []byte(replacement)))
		replacement = escaped.String()
	}
	filled, err := form.Fill(source, pdfer.FormData{"hello": replacement}, nil, false)
	must(err)
	must(os.WriteFile(outputPath, filled, 0o600))

	dataset, _, err := xfa.FindXFADatasetsStream(filled, nil, false)
	must(err)
	wellFormed := validXML(dataset)
	if *mode == "safe" && !wellFormed {
		fatalf("safe mutation produced malformed datasets XML")
	}
	sourceAfter, err := os.ReadFile(inputPath)
	must(err)
	encoded, err := json.MarshalIndent(result{
		Mode:              *mode,
		SourceSHA256:      sourceDigest,
		SourceUnchanged:   digest(sourceAfter) == sourceDigest,
		OutputSHA256:      digest(filled),
		OutputBytes:       len(filled),
		DatasetWellFormed: wellFormed,
		VisibleValue:      *value,
	}, "", "  ")
	must(err)
	fmt.Println(string(encoded))
}

func validXML(data []byte) bool {
	decoder := xml.NewDecoder(bytes.NewReader(data))
	decoder.Strict = true
	for {
		_, err := decoder.Token()
		if err == nil {
			continue
		}
		return errors.Is(err, io.EOF)
	}
}

func digest(data []byte) string {
	value := sha256.Sum256(data)
	return hex.EncodeToString(value[:])
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}

func fatalf(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
