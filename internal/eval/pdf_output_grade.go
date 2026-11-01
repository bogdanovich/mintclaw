package eval

import (
	"os"
	"strings"
)

// The original independent oracle already inspected these exact bytes while
// expected values existed. Regrade preserves its frozen witness, not a new PDF
// inspection. Authorized binary evidence never becomes judge/JSON content.
func pdfOutputArtifactForGrade(root *os.Root, trial Trial, filter *strings.Replacer) ([]byte, error) {
	if trial.PDF == nil || trial.PDF.Artifact == nil || trial.PDF.Settlement == nil ||
		!validID(trial.ID) || trial.PDF.Artifact.File != "artifact-"+trial.ID+".pdf" ||
		trial.PDF.Settlement.ArtifactSize <= 0 || trial.PDF.Settlement.ArtifactSize > maxPDFBytes ||
		!validDigest(trial.PDF.Settlement.ArtifactSHA256) {
		return nil, ErrGrade
	}
	data, err := readPDFNativeFile(root, trial.PDF.Artifact.File, maxPDFBytes)
	if err != nil {
		return nil, ErrGrade
	}
	if int64(len(data)) != trial.PDF.Settlement.ArtifactSize || digest(data) != trial.PDF.Settlement.ArtifactSHA256 ||
		filter != nil && filter.Replace(string(data)) != string(data) {
		clear(data)
		return nil, ErrGrade
	}
	return data, nil
}
