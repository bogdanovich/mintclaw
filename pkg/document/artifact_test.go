package document

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestAdoptWorkerArtifactsPublishesOnlyVerifiedOpaqueRef(t *testing.T) {
	root := directTempDir(t)
	snapshotDir := filepath.Join(root, "operation")
	workerDir := filepath.Join(snapshotDir, ".worker-test")
	if err := os.MkdirAll(workerDir, 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte("{\"page\":1,\"text\":\"MINTCLAW_PDF1A_TEXT\"}\n")
	name := "extracted-text.jsonl"
	if err := os.WriteFile(filepath.Join(workerDir, name), content, 0o600); err != nil {
		t.Fatal(err)
	}
	request, result := artifactTestExtraction(content)
	snapshot := &Snapshot{path: filepath.Join(snapshotDir, "snapshot.pdf"), dir: snapshotDir}
	if err := adoptWorkerArtifacts(snapshot, workerDir, request, &result); err != nil {
		t.Fatalf("adopt artifacts: %v", err)
	}
	if _, err := os.Stat(filepath.Join(workerDir, name)); !os.IsNotExist(err) {
		t.Fatalf("worker artifact survived adoption: %v", err)
	}
	reader, err := snapshot.OpenArtifact(result.Artifacts[0].Artifact.Ref)
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(reader)
	_ = reader.Close()
	if err != nil || string(got) != string(content) {
		t.Fatalf("adopted content = %q, %v", got, err)
	}
	if _, err = snapshot.OpenArtifact("document-artifact://other/extracted-text.jsonl"); err == nil {
		t.Fatal("opened artifact with an unowned opaque ref")
	}
}

func TestAdoptWorkerArtifactsRejectsTamperingAndUndeclaredFiles(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(string, *WorkerResult) error
	}{
		{
			name: "digest mismatch",
			mutate: func(_ string, result *WorkerResult) error {
				result.Artifacts[0].Artifact.SHA256 = "00" + result.Artifacts[0].Artifact.SHA256[2:]
				return nil
			},
		},
		{
			name: "undeclared file",
			mutate: func(workerDir string, _ *WorkerResult) error {
				return os.WriteFile(filepath.Join(workerDir, "surprise.txt"), []byte("unexpected"), 0o600)
			},
		},
		{
			name: "symlink",
			mutate: func(workerDir string, _ *WorkerResult) error {
				if runtime.GOOS == "windows" {
					return nil
				}
				path := filepath.Join(workerDir, "extracted-text.jsonl")
				if err := os.Remove(path); err != nil {
					return err
				}
				return os.Symlink("/etc/passwd", path)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if test.name == "symlink" && runtime.GOOS == "windows" {
				t.Skip("symlink setup is platform-specific")
			}
			root := directTempDir(t)
			snapshotDir := filepath.Join(root, "operation")
			workerDir := filepath.Join(snapshotDir, ".worker-test")
			if err := os.MkdirAll(workerDir, 0o700); err != nil {
				t.Fatal(err)
			}
			content := []byte("{\"page\":1,\"text\":\"marker\"}\n")
			if err := os.WriteFile(filepath.Join(workerDir, "extracted-text.jsonl"), content, 0o600); err != nil {
				t.Fatal(err)
			}
			request, result := artifactTestExtraction(content)
			if err := test.mutate(workerDir, &result); err != nil {
				t.Fatal(err)
			}
			snapshot := &Snapshot{path: filepath.Join(snapshotDir, "snapshot.pdf"), dir: snapshotDir}
			if err := adoptWorkerArtifacts(snapshot, workerDir, request, &result); err == nil {
				t.Fatal("adopted invalid worker artifacts")
			}
			if snapshot.artifactPaths != nil {
				t.Fatalf("invalid artifact was registered: %#v", snapshot.artifactPaths)
			}
		})
	}
}

func TestWorkerProtocolRejectsUntrustedArtifactDescriptors(t *testing.T) {
	content := []byte("{\"page\":1,\"text\":\"marker\"}\n")
	request, valid := artifactTestExtraction(content)
	tests := []struct {
		name   string
		mutate func(*WorkerResult)
	}{
		{
			name:   "ref",
			mutate: func(result *WorkerResult) { result.Artifacts[0].Artifact.Ref = "document-artifact://other" },
		},
		{name: "name", mutate: func(result *WorkerResult) { result.Artifacts[0].Name = "../escape" }},
		{name: "kind", mutate: func(result *WorkerResult) { result.Artifacts[0].Artifact.Kind = "other" }},
		{name: "MIME", mutate: func(result *WorkerResult) { result.Artifacts[0].Artifact.ContentType = "text/plain" }},
		{
			name:   "source",
			mutate: func(result *WorkerResult) { result.Artifacts[0].Artifact.SourceSHA256 = strings.Repeat("b", 64) },
		},
		{name: "page", mutate: func(result *WorkerResult) { result.Artifacts[0].Artifact.Pages = []int{2} }},
		{name: "size", mutate: func(result *WorkerResult) {
			result.Artifacts[0].Artifact.Size = DefaultMaxArtifactBytes + 1
		}},
		{name: "digest", mutate: func(result *WorkerResult) { result.Artifacts[0].Artifact.SHA256 = "invalid" }},
		{name: "backend", mutate: func(result *WorkerResult) { result.Extraction.Backend.Name = "other" }},
		{name: "content in control", mutate: func(result *WorkerResult) {
			result.Artifacts[0].Artifact.Kind = "marker secret content"
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := cloneWorkerResult(t, valid)
			test.mutate(&result)
			encoded, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = decodeWorkerResult(encoded, request); err == nil {
				t.Fatalf("accepted untrusted worker descriptor: %#v", result)
			}
		})
	}
}

func TestAdoptWorkerArtifactsRejectsContentThatContradictsFacts(t *testing.T) {
	root := directTempDir(t)
	snapshotDir := filepath.Join(root, "operation")
	workerDir := filepath.Join(snapshotDir, ".worker-test")
	if err := os.MkdirAll(workerDir, 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte("{\"page\":1,\"text\":\"marker\",\"secret_path\":\"/private/input\"}\n")
	if err := os.WriteFile(filepath.Join(workerDir, "extracted-text.jsonl"), content, 0o600); err != nil {
		t.Fatal(err)
	}
	request, result := artifactTestExtraction(content)
	result.Extraction.Pages[0].Characters = 6
	result.Extraction.TotalCharacters = 6
	snapshot := &Snapshot{path: filepath.Join(snapshotDir, "snapshot.pdf"), dir: snapshotDir}
	if err := adoptWorkerArtifacts(snapshot, workerDir, request, &result); err == nil {
		t.Fatal("adopted artifact containing unknown content fields")
	}
}

func TestValidateRenderedArtifactDecodesCompletePNG(t *testing.T) {
	var encoded bytes.Buffer
	pixels := image.NewRGBA(image.Rect(0, 0, 2, 3))
	pixels.Set(0, 0, color.RGBA{R: 0xff, A: 0xff})
	if err := png.Encode(&encoded, pixels); err != nil {
		t.Fatal(err)
	}
	result := &WorkerResult{Rendering: &RenderingFacts{}}
	artifact := Artifact{Pages: []int{1}, Width: 2, Height: 3}
	if err := validateRenderedArtifact(encoded.Bytes(), result, artifact); err != nil {
		t.Fatalf("valid PNG rejected: %v", err)
	}
	truncated := encoded.Bytes()[:encoded.Len()-8]
	if err := validateRenderedArtifact(truncated, result, artifact); err == nil {
		t.Fatal("accepted PNG with a truncated IEND chunk")
	}
	withTrailingData := append(append([]byte(nil), encoded.Bytes()...), []byte("trailing")...)
	if err := validateRenderedArtifact(withTrailingData, result, artifact); err == nil {
		t.Fatal("accepted PNG with trailing data")
	}
}

func artifactTestExtraction(content []byte) (WorkerRequest, WorkerResult) {
	input := DocumentRef{
		Ref: "document://local/document_operation_artifact_test", ContentType: "application/pdf",
		Size: 10, SHA256: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	read := WorkerReadRequest{Pages: []int{1}, Limits: defaultReadLimits(workerOperationExtract)}
	request := newReadWorkerRequest(input, defaultInspectionLimits(), workerOperationExtract, read)
	digest := sha256.Sum256(content)
	descriptor := Artifact{
		Ref:  workerArtifactRef(request.OperationID, "extracted-text.jsonl"),
		Kind: "extracted_text", ContentType: "application/x-ndjson", Size: int64(len(content)),
		SHA256: hex.EncodeToString(digest[:]), SourceSHA256: request.Input.SHA256, Pages: []int{1},
	}
	return request, WorkerResult{
		SchemaVersion: WorkerResultSchemaVersion, OperationID: request.OperationID, State: StateSucceeded,
		Input: &request.Input,
		Extraction: &ExtractionFacts{
			Backend: popplerIdentity(), SelectedPages: []int{1},
			Pages:           []PageTextFacts{{Page: 1, Characters: runeCountInArtifact(content)}},
			TotalCharacters: runeCountInArtifact(content),
		},
		Artifacts: []WorkerArtifact{{Name: "extracted-text.jsonl", Artifact: descriptor}},
	}
}

func runeCountInArtifact(content []byte) int {
	const prefix = "{\"page\":1,\"text\":\""
	const suffix = "\"}\n"
	return len([]rune(string(content[len(prefix) : len(content)-len(suffix)])))
}

func cloneWorkerResult(t *testing.T, input WorkerResult) WorkerResult {
	t.Helper()
	encoded, err := json.Marshal(input)
	if err != nil {
		t.Fatal(err)
	}
	var result WorkerResult
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	if err = decoder.Decode(&result); err != nil {
		t.Fatal(err)
	}
	return result
}
