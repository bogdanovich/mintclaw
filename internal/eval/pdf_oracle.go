package eval

import (
	"bytes"
	"context"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

//go:embed pdf_oracle.py
var pdfOracleScript []byte

const (
	pdfOracleVersion = "mintclaw.eval.pdf_oracle.v1"
	maxPDFBytes      = 64 * 1024 * 1024
	maxOracleOutput  = 8192
)

var ErrPDFOracle = errors.New("eval: independent PDF oracle unavailable or invalid")

type pdfOracleRequest struct {
	Version      int        `json:"version"`
	SourceSize   int        `json:"source_size"`
	ArtifactSize int        `json:"artifact_size"`
	SourceSHA256 string     `json:"source_sha256"`
	Pages        int        `json:"pages"`
	FieldCount   int        `json:"field_count"`
	Fields       []PDFField `json:"fields"`
}

// PDFOracleResult contains predicates and counts only, never field values or
// parser diagnostics. It proves neither native approval nor logical delivery.
type PDFOracleResult struct {
	Version           int    `json:"version"`
	Schema            string `json:"schema"`
	Code              string `json:"code"`
	Pypdf             string `json:"pypdf"`
	PyMuPDF           string `json:"pymupdf"`
	MuPDF             string `json:"mupdf"`
	SourceSHA256      string `json:"source_sha256"`
	ArtifactSHA256    string `json:"artifact_sha256"`
	FieldsMatched     bool   `json:"fields_matched"`
	StructureMatched  bool   `json:"structure_matched"`
	AppearanceMatched bool   `json:"appearance_matched"`
	BackgroundMatched bool   `json:"background_matched"`
	CheckedFields     int    `json:"checked_fields"`
	RenderedPages     int    `json:"rendered_pages"`
	OracleSHA256      string `json:"oracle_sha256,omitempty"`
}

// InspectPDFArtifact invokes only the embedded, pinned independent oracle.
// Caller-admitted bytes and resolved field expectations travel over stdin, not
// argv, files, environment or model context. No production PDF engine is used.
func InspectPDFArtifact(
	ctx context.Context, python string, source, artifact []byte, sourceDigest string, pdf PDFCase,
) (PDFOracleResult, error) {
	request := pdfOracleRequest{
		Version: Version, SourceSize: len(source), ArtifactSize: len(artifact),
		SourceSHA256: sourceDigest, Pages: pdf.Pages, FieldCount: pdf.FieldCount, Fields: pdf.Fields,
	}
	if !filepath.IsAbs(python) || len(source) == 0 || len(source) > maxPDFBytes ||
		len(artifact) == 0 || len(artifact) > maxPDFBytes || !validDigest(sourceDigest) ||
		digest(source) != sourceDigest || pdf.Pages < 1 || pdf.Pages > 32 ||
		pdf.FieldCount < 1 || pdf.FieldCount > 256 || len(pdf.Fields) != pdf.FieldCount {
		return PDFOracleResult{}, ErrPDFOracle
	}
	for _, field := range pdf.Fields {
		if field.Fact != "" {
			// The caller must resolve handles ephemerally before oracle admission.
			return PDFOracleResult{}, ErrPDFOracle
		}
	}
	info, err := os.Stat(python)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return PDFOracleResult{}, ErrPDFOracle
	}
	metadata, err := json.Marshal(request)
	if err != nil || len(metadata) > MaxManifestBytes {
		return PDFOracleResult{}, ErrPDFOracle
	}
	defer clear(metadata)
	ctx, cancel := context.WithTimeout(ctx, time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, python, "-I", "-c", string(pdfOracleScript))
	command.Env = []string{"PATH=" + filepath.Dir(python) + string(os.PathListSeparator) + "/usr/bin:/bin"}
	command.Stdin = io.MultiReader(
		bytes.NewReader(metadata), strings.NewReader("\n"), bytes.NewReader(source), bytes.NewReader(artifact),
	)
	output := &boundedOracleOutput{}
	defer func() { clear(output.data) }()
	command.Stdout, command.Stderr = output, io.Discard
	command.WaitDelay = 2 * time.Second
	if command.Run() != nil {
		return PDFOracleResult{}, ErrPDFOracle
	}
	result, err := DecodeManifest[PDFOracleResult](output.data)
	if err != nil || result.Version != Version || result.Schema != pdfOracleVersion ||
		result.Pypdf != "6.20.0" || result.PyMuPDF != "1.28.2" || !boundedIdentity(result.MuPDF, 64) ||
		result.OracleSHA256 != "" ||
		result.SourceSHA256 != sourceDigest || result.ArtifactSHA256 != digest(artifact) ||
		!slices.Contains([]string{"matched", "mismatch"}, result.Code) ||
		result.CheckedFields < 0 || result.CheckedFields > pdf.FieldCount ||
		result.RenderedPages < 0 || result.RenderedPages > pdf.Pages ||
		(result.Code == "matched") != (result.FieldsMatched && result.StructureMatched &&
			result.AppearanceMatched && result.BackgroundMatched &&
			result.CheckedFields == pdf.FieldCount && result.RenderedPages == pdf.Pages) {
		return PDFOracleResult{}, ErrPDFOracle
	}
	hash := sha256.Sum256(pdfOracleScript)
	result.OracleSHA256 = hex.EncodeToString(hash[:])
	return result, nil
}

type boundedOracleOutput struct{ data []byte }

func (output *boundedOracleOutput) Write(data []byte) (int, error) {
	if len(data) > maxOracleOutput-len(output.data) {
		return 0, ErrPDFOracle
	}
	output.data = append(output.data, data...)
	return len(data), nil
}
