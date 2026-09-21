package document

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"

	"github.com/bogdanovich/mintclaw/pkg/fileutil"
)

const (
	formWriteGenerationSchemaVersion = "mintclaw.document_write_generation.v1"
	maximumFormWriteGenerationBytes  = 32 * 1024
	formWriteCandidateFilename       = "candidate.pdf"
	formWriteGenerationFilename      = "generation.json"
)

var (
	errFormWriteGenerationFailed    = errors.New("document write generation is unavailable")
	errFormWriteGenerationUncertain = errors.New("document write generation durability is uncertain")
)

// formWriteGeneration is a private, value-free commit marker for one output
// generation. Its presence means candidate.pdf was written and synced first.
type formWriteGeneration struct {
	SchemaVersion string         `json:"schema_version"`
	OperationID   string         `json:"operation_id"`
	SourceSHA256  string         `json:"source_sha256"`
	RequestSHA256 string         `json:"request_sha256"`
	Artifact      Artifact       `json:"artifact"`
	Facts         FormWriteFacts `json:"facts"`
}

type formWriteStore struct {
	root      string
	writeFile func(string, []byte, os.FileMode) error
}

func newFormWriteStore(root string) (*formWriteStore, error) {
	resolved, err := prepareWriteJournalRoot(root)
	if err != nil {
		return nil, errFormWriteGenerationFailed
	}
	return &formWriteStore{root: resolved, writeFile: fileutil.WriteFileAtomic}, nil
}

func (store *formWriteStore) executionLockPath(operationID string) string {
	return filepath.Join(store.root, operationID+".lock")
}

func (store *formWriteStore) candidatePath(operationID string) string {
	return filepath.Join(store.root, operationID, formWriteCandidateFilename)
}

func (store *formWriteStore) generationPath(operationID string) string {
	return filepath.Join(store.root, operationID, formWriteGenerationFilename)
}

func (store *formWriteStore) Commit(
	operationID string,
	snapshot *Snapshot,
	result WorkerResult,
) (formWriteGeneration, error) {
	if store == nil || snapshot == nil || !validWriteOperationID(operationID) ||
		result.State != StateSucceeded || result.Write == nil || len(result.Artifacts) != 1 {
		return formWriteGeneration{}, errFormWriteGenerationFailed
	}
	workerArtifact := result.Artifacts[0]
	if workerArtifact.Artifact.Ref != workerArtifactRef(operationID, filledCandidateArtifactName) {
		return formWriteGeneration{}, errFormWriteGenerationFailed
	}
	generation := formWriteGeneration{
		SchemaVersion: formWriteGenerationSchemaVersion,
		OperationID:   operationID,
		SourceSHA256:  result.Write.SourceSHA256,
		RequestSHA256: result.Write.RequestSHA256,
		Artifact:      workerArtifact.Artifact,
		Facts:         *result.Write,
	}
	if !validFormWriteGeneration(generation) {
		return formWriteGeneration{}, errFormWriteGenerationFailed
	}
	if existing, found, err := store.Load(operationID); err != nil {
		return formWriteGeneration{}, err
	} else if found {
		if !equalFormWriteGeneration(existing, generation) {
			return formWriteGeneration{}, ErrWriteConflict
		}
		return existing, nil
	}

	operationRoot, err := prepareWriteJournalRoot(filepath.Join(store.root, operationID))
	if err != nil {
		return formWriteGeneration{}, errFormWriteGenerationFailed
	}
	content, err := readFormWriteArtifact(snapshot, generation.Artifact)
	if err != nil {
		return formWriteGeneration{}, err
	}
	candidatePath := filepath.Join(operationRoot, formWriteCandidateFilename)
	if err = store.writeFile(candidatePath, content, 0o600); err != nil {
		if fileutil.IsCommittedWriteError(err) {
			return formWriteGeneration{}, errFormWriteGenerationUncertain
		}
		return formWriteGeneration{}, errFormWriteGenerationFailed
	}
	encoded, err := json.MarshalIndent(generation, "", "  ")
	if err != nil || len(encoded)+1 > maximumFormWriteGenerationBytes {
		return formWriteGeneration{}, errFormWriteGenerationFailed
	}
	encoded = append(encoded, '\n')
	if err = store.writeFile(filepath.Join(operationRoot, formWriteGenerationFilename), encoded, 0o600); err != nil {
		if fileutil.IsCommittedWriteError(err) {
			return formWriteGeneration{}, errFormWriteGenerationUncertain
		}
		return formWriteGeneration{}, errFormWriteGenerationUncertain
	}
	return generation, nil
}

func (store *formWriteStore) Load(operationID string) (formWriteGeneration, bool, error) {
	if store == nil || !validWriteOperationID(operationID) {
		return formWriteGeneration{}, false, errFormWriteGenerationFailed
	}
	candidatePath := store.candidatePath(operationID)
	generationPath := store.generationPath(operationID)
	candidateInfo, candidateErr := os.Lstat(candidatePath)
	generationInfo, generationErr := os.Lstat(generationPath)
	if errors.Is(candidateErr, os.ErrNotExist) && errors.Is(generationErr, os.ErrNotExist) {
		return formWriteGeneration{}, false, nil
	}
	if candidateErr != nil || generationErr != nil || candidateInfo.Mode()&os.ModeSymlink != 0 ||
		generationInfo.Mode()&os.ModeSymlink != 0 || !candidateInfo.Mode().IsRegular() ||
		!generationInfo.Mode().IsRegular() {
		return formWriteGeneration{}, false, errFormWriteGenerationUncertain
	}
	file, err := openSourceNoFollow(generationPath)
	if err != nil {
		return formWriteGeneration{}, false, errFormWriteGenerationUncertain
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil || generationInfo.Size() <= 0 || generationInfo.Size() > maximumFormWriteGenerationBytes ||
		validateDocumentJournalRecordSecurity(generationPath, file, opened) != nil {
		return formWriteGeneration{}, false, errFormWriteGenerationUncertain
	}
	data, err := io.ReadAll(io.LimitReader(file, maximumFormWriteGenerationBytes+1))
	if err != nil || len(data) > maximumFormWriteGenerationBytes {
		return formWriteGeneration{}, false, errFormWriteGenerationUncertain
	}
	var generation formWriteGeneration
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&generation); err != nil {
		return formWriteGeneration{}, false, errFormWriteGenerationUncertain
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) || generation.OperationID != operationID ||
		!validFormWriteGeneration(generation) {
		return formWriteGeneration{}, false, errFormWriteGenerationUncertain
	}
	digest, size, err := digestFormWriteCandidate(candidatePath, generation.Artifact.Size)
	if err != nil || digest != generation.Artifact.SHA256 || size != generation.Artifact.Size {
		return formWriteGeneration{}, false, errFormWriteGenerationUncertain
	}
	return generation, true, nil
}

// Materialize gives one caller snapshot its own immutable copy of the
// committed generation. It revalidates bytes from the opened source handle so
// the earlier Load check cannot be separated from handoff by a path swap.
func (store *formWriteStore) Materialize(snapshot *Snapshot, generation formWriteGeneration) error {
	if store == nil || snapshot == nil || snapshot.dir == "" || !validFormWriteGeneration(generation) {
		return errFormWriteGenerationFailed
	}
	content, err := readFormWriteCandidatePath(store.candidatePath(generation.OperationID), generation.Artifact)
	if err != nil {
		return errFormWriteGenerationUncertain
	}
	operationRoot, err := prepareWriteJournalRoot(
		filepath.Join(snapshot.dir, "form-write", generation.OperationID),
	)
	if err != nil {
		return errFormWriteGenerationFailed
	}
	target := filepath.Join(operationRoot, filledCandidateArtifactName)
	if err = fileutil.WriteFileAtomic(target, content, 0o400); err != nil {
		return errFormWriteGenerationFailed
	}
	if snapshot.artifactPaths == nil {
		snapshot.artifactPaths = make(map[string]string, 1)
	}
	snapshot.artifactPaths[generation.Artifact.Ref] = target
	return nil
}

func readFormWriteArtifact(snapshot *Snapshot, artifact Artifact) ([]byte, error) {
	file, err := snapshot.OpenArtifact(artifact.Ref)
	if err != nil {
		return nil, errFormWriteGenerationFailed
	}
	defer func() { _ = file.Close() }()
	data, err := io.ReadAll(io.LimitReader(file, DefaultMaxArtifactBytes+1))
	if err != nil || int64(len(data)) != artifact.Size || int64(len(data)) > DefaultMaxArtifactBytes {
		return nil, errFormWriteGenerationFailed
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != artifact.SHA256 {
		return nil, errFormWriteGenerationFailed
	}
	return data, nil
}

func digestFormWriteCandidate(path string, maximum int64) (string, int64, error) {
	if maximum <= 0 || maximum > DefaultMaxArtifactBytes {
		return "", 0, errFormWriteGenerationFailed
	}
	file, err := openSourceNoFollow(path)
	if err != nil {
		return "", 0, errFormWriteGenerationFailed
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || opened.Size() != maximum ||
		validateDocumentJournalRecordSecurity(path, file, opened) != nil {
		return "", 0, errFormWriteGenerationFailed
	}
	hash := sha256.New()
	size, err := io.Copy(hash, io.LimitReader(file, maximum+1))
	if err != nil || size != maximum {
		return "", 0, errFormWriteGenerationFailed
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}

func readFormWriteCandidatePath(path string, artifact Artifact) ([]byte, error) {
	if artifact.Size <= 0 || artifact.Size > DefaultMaxArtifactBytes || !validDocumentDigest(artifact.SHA256) {
		return nil, errFormWriteGenerationFailed
	}
	file, err := openSourceNoFollow(path)
	if err != nil {
		return nil, errFormWriteGenerationFailed
	}
	defer func() { _ = file.Close() }()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || opened.Size() != artifact.Size ||
		validateDocumentJournalRecordSecurity(path, file, opened) != nil {
		return nil, errFormWriteGenerationFailed
	}
	data, err := io.ReadAll(io.LimitReader(file, artifact.Size+1))
	if err != nil || int64(len(data)) != artifact.Size {
		return nil, errFormWriteGenerationFailed
	}
	digest := sha256.Sum256(data)
	if hex.EncodeToString(digest[:]) != artifact.SHA256 {
		return nil, errFormWriteGenerationFailed
	}
	return data, nil
}

func validFormWriteGeneration(generation formWriteGeneration) bool {
	facts := generation.Facts
	artifact := generation.Artifact
	if generation.SchemaVersion != formWriteGenerationSchemaVersion ||
		!validWriteOperationID(generation.OperationID) || !validDocumentDigest(generation.SourceSHA256) ||
		!validDocumentDigest(generation.RequestSHA256) || !validDocumentDigest(facts.OutputSHA256) ||
		generation.SourceSHA256 != facts.SourceSHA256 || generation.RequestSHA256 != facts.RequestSHA256 ||
		!validFormWriteFactEnvelope(facts) {
		return false
	}
	return artifact.Ref == workerArtifactRef(generation.OperationID, filledCandidateArtifactName) &&
		artifact.Kind == filledCandidateArtifactKind && artifact.ContentType == "application/pdf" &&
		artifact.Size == facts.OutputSize && artifact.SHA256 == facts.OutputSHA256 &&
		artifact.SourceSHA256 == facts.SourceSHA256 && equalPages(artifact.Pages, facts.AffectedPages) &&
		artifact.Width == 0 && artifact.Height == 0 && !artifact.Truncated &&
		validWriteVerification(formWriteVerificationEvidence(facts))
}

func validFormWriteGenerationPages(pages []int) bool {
	if len(pages) == 0 || len(pages) > DefaultMaxRenderPages {
		return false
	}
	previous := 0
	for _, page := range pages {
		if page <= previous || page > DefaultMaxPages {
			return false
		}
		previous = page
	}
	return true
}

func equalFormWriteGeneration(left, right formWriteGeneration) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}

func formWriteVerificationEvidence(facts FormWriteFacts) WriteVerificationEvidence {
	return WriteVerificationEvidence{
		StructuralAssertions: facts.StructuralAssertions,
		VisualAssertions:     facts.VisualAssertions,
		CheckedFields:        facts.CheckedFields,
		CheckedWidgets:       facts.CheckedWidgets,
		UnchangedFields:      facts.UnchangedFields,
		RenderedPages:        facts.RenderedPages,
	}
}
