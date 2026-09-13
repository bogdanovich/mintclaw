package oauthprovider

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/openai/openai-go/v3"
)

func TestCodexProviderPublishesImageGenerationCapability(t *testing.T) {
	provider := NewCodexProvider("test-token", "acct-123")
	capabilities := provider.Capabilities()
	if !capabilities.ImageGeneration.Supported {
		t.Fatal("image generation capability = false, want true")
	}
	if capabilities.ImageGeneration.ProviderID != "openai-codex" {
		t.Fatalf("provider id = %q, want openai-codex", capabilities.ImageGeneration.ProviderID)
	}
	if capabilities.ImageGeneration.DefaultModel != "gpt-image-2" {
		t.Fatalf("default image model = %q, want gpt-image-2", capabilities.ImageGeneration.DefaultModel)
	}
	if !capabilities.ImageGeneration.Editing ||
		capabilities.ImageGeneration.MaxInputImages != maxImageEditInputs ||
		capabilities.ImageGeneration.MaxInputBytes != maxImageEditInputBytes {
		t.Fatalf("image edit capabilities = %+v, want provider limits", capabilities.ImageGeneration)
	}
}

func TestBuildCodexImageParams(t *testing.T) {
	params := buildCodexImageParams(ImageGenerationRequest{
		Prompt:       "make a tiny icon",
		Model:        "gpt-image-2",
		Size:         "1536x1024",
		Quality:      "medium",
		OutputFormat: "png",
		Count:        2,
	})
	data, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	payload := string(data)
	for _, want := range []string{
		`"prompt":"make a tiny icon"`,
		`"model":"gpt-image-2"`,
		`"size":"1536x1024"`,
		`"quality":"medium"`,
		`"output_format":"png"`,
		`"n":2`,
	} {
		if !strings.Contains(payload, want) {
			t.Fatalf("payload missing %s: %s", want, payload)
		}
	}
	for _, forbidden := range []string{`"tools"`, `"tool_choice"`, `"instructions"`, "gpt-5.4"} {
		if strings.Contains(payload, forbidden) {
			t.Fatalf("standalone image payload contains %q: %s", forbidden, payload)
		}
	}
}

func TestCodexProviderGeneratesViaImagesEndpoint(t *testing.T) {
	payload := base64.StdEncoding.EncodeToString([]byte("fake-webp"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/images/generations" {
			t.Errorf("request = %s %s, want POST /images/generations", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer fresh-token" {
			t.Errorf("Authorization = %q, want refreshed bearer token", got)
		}
		if got := r.Header.Get("Chatgpt-Account-Id"); got != "fresh-account" {
			t.Errorf("Chatgpt-Account-Id = %q, want refreshed account", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		if body["model"] != "gpt-image-2" || body["prompt"] != "make an icon" {
			t.Errorf("request body = %#v", body)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(
			w,
			`{"created":1,"data":[{"b64_json":%q}],"output_format":"webp","quality":"medium","size":"1024x1024"}`,
			payload,
		)
	}))
	defer server.Close()

	provider := NewCodexProvider("stale-token", "stale-account")
	provider.client = createOpenAITestClient(server.URL, "stale-token", "stale-account")
	provider.tokenSource = func() (string, string, error) {
		return "fresh-token", "fresh-account", nil
	}
	response, err := provider.GenerateImage(t.Context(), ImageGenerationRequest{
		Prompt:       "make an icon",
		Model:        "gpt-image-2",
		Size:         "1024x1024",
		Quality:      "medium",
		OutputFormat: "png",
		Count:        1,
	})
	if err != nil {
		t.Fatalf("GenerateImage() error = %v", err)
	}
	if len(response.Images) != 1 {
		t.Fatalf("images = %d, want 1", len(response.Images))
	}
	image := response.Images[0]
	if string(image.Data) != "fake-webp" || image.MimeType != "image/webp" || image.Ext != "webp" {
		t.Fatalf("image = %#v, want decoded webp response", image)
	}
}

func TestCodexProviderEditsViaImagesEndpoint(t *testing.T) {
	payload := base64.StdEncoding.EncodeToString([]byte("edited-png"))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/images/edits" {
			t.Errorf("request = %s %s, want POST /images/edits", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer fresh-token" {
			t.Errorf("Authorization = %q, want refreshed bearer token", got)
		}
		if got := r.Header.Get("Chatgpt-Account-Id"); got != "fresh-account" {
			t.Errorf("Chatgpt-Account-Id = %q, want refreshed account", got)
		}
		if err := r.ParseMultipartForm(2 * 1024 * 1024); err != nil {
			t.Fatalf("ParseMultipartForm() error = %v", err)
		}
		for field, want := range map[string]string{
			"prompt":         "translate the caption",
			"model":          "gpt-image-2",
			"size":           "auto",
			"quality":        "high",
			"output_format":  "png",
			"input_fidelity": "high",
			"n":              "1",
		} {
			if got := r.FormValue(field); got != want {
				t.Errorf("form field %s = %q, want %q", field, got, want)
			}
		}
		files := r.MultipartForm.File["image"]
		if len(files) != 1 {
			t.Fatalf("image uploads = %d, want 1; form = %#v", len(files), r.MultipartForm.File)
		}
		if files[0].Filename != "source.jpg" || files[0].Header.Get("Content-Type") != "image/jpeg" {
			t.Errorf("image upload metadata = filename %q content-type %q", files[0].Filename,
				files[0].Header.Get("Content-Type"))
		}
		file, err := files[0].Open()
		if err != nil {
			t.Fatalf("uploaded image Open() error = %v", err)
		}
		data, err := io.ReadAll(file)
		if err != nil {
			t.Fatalf("uploaded image ReadAll() error = %v", err)
		}
		if err := file.Close(); err != nil {
			t.Errorf("uploaded image Close() error = %v", err)
		}
		if string(data) != "actual-source-bytes" {
			t.Errorf("uploaded image = %q, want exact source bytes", data)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(
			w,
			`{"created":1,"data":[{"b64_json":%q}],"output_format":"png","quality":"high","size":"1024x1024"}`,
			payload,
		)
	}))
	defer server.Close()

	provider := NewCodexProvider("stale-token", "stale-account")
	provider.client = createOpenAITestClient(server.URL, "stale-token", "stale-account")
	provider.tokenSource = func() (string, string, error) {
		return "fresh-token", "fresh-account", nil
	}
	response, err := provider.GenerateImage(t.Context(), ImageGenerationRequest{
		Prompt:        "translate the caption",
		Model:         "gpt-image-2",
		Size:          "auto",
		Quality:       "high",
		OutputFormat:  "png",
		Count:         1,
		InputFidelity: "high",
		InputImages: []ImageGenerationInput{{
			Data:        []byte("actual-source-bytes"),
			Filename:    "source.jpg",
			ContentType: "image/jpeg",
		}},
	})
	if err != nil {
		t.Fatalf("GenerateImage() edit error = %v", err)
	}
	if len(response.Images) != 1 || string(response.Images[0].Data) != "edited-png" {
		t.Fatalf("edited images = %#v, want decoded response", response.Images)
	}
}

func TestCodexProviderRejectsTooManyEditInputs(t *testing.T) {
	provider := NewCodexProvider("token", "account")
	inputs := make([]ImageGenerationInput, maxImageEditInputs+1)
	_, err := provider.GenerateImage(t.Context(), ImageGenerationRequest{
		Prompt:      "combine",
		InputImages: inputs,
	})
	if err == nil || !strings.Contains(err.Error(), "input image limit") {
		t.Fatalf("GenerateImage() error = %v, want input limit", err)
	}
}

func TestCodexProviderEnforcesEditByteBoundary(t *testing.T) {
	payload := base64.StdEncoding.EncodeToString([]byte("edited-png"))
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/images/edits" {
			t.Errorf("request = %s %s, want POST /images/edits", r.Method, r.URL.Path)
		}
		if _, err := io.Copy(io.Discard, r.Body); err != nil {
			t.Errorf("read request body: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"data":[{"b64_json":%q}],"output_format":"png"}`, payload)
	}))
	defer server.Close()

	provider := NewCodexProvider("token", "account")
	provider.client = createOpenAITestClient(server.URL, "token", "account")
	input := make([]byte, maxImageEditInputBytes+1)
	_, err := provider.GenerateImage(t.Context(), ImageGenerationRequest{
		Prompt: "preserve source",
		InputImages: []ImageGenerationInput{{
			Data:        input[:maxImageEditInputBytes],
			Filename:    "boundary.png",
			ContentType: "image/png",
		}},
	})
	if err != nil {
		t.Fatalf("GenerateImage() at byte boundary error = %v", err)
	}
	_, err = provider.GenerateImage(t.Context(), ImageGenerationRequest{
		Prompt: "preserve source",
		InputImages: []ImageGenerationInput{{
			Data:        input,
			Filename:    "over-limit.png",
			ContentType: "image/png",
		}},
	})
	if err == nil || !strings.Contains(err.Error(), "input 1 exceeded byte limit") {
		t.Fatalf("GenerateImage() over byte boundary error = %v, want byte limit", err)
	}
	if got := requests.Load(); got != 1 {
		t.Fatalf("edit HTTP requests = %d, want only the boundary request", got)
	}
}

func TestDecodeCodexImagesBoundsAndCapsResults(t *testing.T) {
	encoded := base64.StdEncoding.EncodeToString([]byte("image"))
	response := &openai.ImagesResponse{OutputFormat: openai.ImagesResponseOutputFormatPNG}
	for range maxImageGenerationResults + 1 {
		response.Data = append(response.Data, openai.Image{B64JSON: encoded})
	}

	images, err := decodeCodexImages(response, "jpeg", len(encoded)*maxImageGenerationResults)
	if err != nil {
		t.Fatalf("decodeCodexImages() error = %v", err)
	}
	if len(images) != maxImageGenerationResults {
		t.Fatalf("images = %d, want cap %d", len(images), maxImageGenerationResults)
	}
	if images[0].MimeType != "image/png" {
		t.Fatalf("response format was not authoritative: %#v", images[0])
	}

	_, err = decodeCodexImages(response, "png", len(encoded)-1)
	if err == nil || !strings.Contains(err.Error(), "exceeded size limit") {
		t.Fatalf("oversized response error = %v", err)
	}
}

func TestDecodeCodexImagesRejectsMalformedPayload(t *testing.T) {
	_, err := decodeCodexImages(
		&openai.ImagesResponse{Data: []openai.Image{{B64JSON: "not-base64"}}},
		"png",
		1024,
	)
	if err == nil || !strings.Contains(err.Error(), "decode codex image response") {
		t.Fatalf("malformed payload error = %v", err)
	}
}

func TestLimitedResponseBodyRejectsOversizedPayload(t *testing.T) {
	body := &limitedResponseBody{
		ReadCloser: io.NopCloser(strings.NewReader("123456789")),
		remaining:  8,
	}
	_, err := io.ReadAll(body)
	if !errors.Is(err, errCodexImageResponseTooLarge) {
		t.Fatalf("ReadAll() error = %v, want bounded response error", err)
	}
}
