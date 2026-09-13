package httpapi

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestGeminiProviderPublishesImageGenerationCapabilities(t *testing.T) {
	capabilities := NewGeminiProvider("key", "", "", "", 0, nil, nil).Capabilities().ImageGeneration
	if !capabilities.Supported || !capabilities.Editing || capabilities.ProviderID != "gemini" ||
		capabilities.DefaultModel != geminiDefaultImageGenerationModel ||
		capabilities.MaxResults != geminiMaxImageResults ||
		capabilities.MaxInputImages != geminiMaxInputImages ||
		capabilities.MaxInputBytes != geminiMaxInputBytes {
		t.Fatalf("image generation capabilities = %+v", capabilities)
	}
}

func TestGeminiGenerateImageUsesInteractionsEndpointAndNormalizesPortableHints(t *testing.T) {
	pngBytes := testGeminiPNG(t)
	var body map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/interactions" {
			t.Errorf("request = %s %s, want POST /interactions", request.Method, request.URL.Path)
		}
		if got := request.Header.Get("X-Goog-Api-Key"); got != "gemini-secret" {
			t.Errorf("X-Goog-Api-Key = %q", got)
		}
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		writeGeminiImageResponse(t, writer, pngBytes, "image/png")
	}))
	t.Cleanup(server.Close)

	provider := NewGeminiProvider("gemini-secret", server.URL, "", "", 0, nil, nil)
	response, err := provider.GenerateImage(t.Context(), ImageGenerationRequest{
		Prompt:        "Draw a mint claw",
		Model:         "gemini-3.1-flash-image",
		Size:          "1024x1024",
		Quality:       "high",
		OutputFormat:  "webp",
		Count:         1,
		InputFidelity: "high",
	})
	if err != nil {
		t.Fatalf("GenerateImage() error = %v", err)
	}
	if len(response.Images) != 1 || !bytes.Equal(response.Images[0].Data, pngBytes) ||
		response.Images[0].MimeType != "image/png" || response.Images[0].Ext != "png" {
		t.Fatalf("images = %#v", response.Images)
	}
	if body["model"] != "gemini-3.1-flash-image" || body["store"] != false {
		t.Fatalf("request body = %#v", body)
	}
	if _, ok := body["quality"]; ok {
		t.Fatalf("portable quality leaked into Gemini request: %#v", body)
	}
	if _, ok := body["input_fidelity"]; ok {
		t.Fatalf("portable input_fidelity leaked into Gemini request: %#v", body)
	}
	format := body["response_format"].(map[string]any)
	if format["mime_type"] != "image/png" || format["aspect_ratio"] != "1:1" || format["image_size"] != "1K" {
		t.Fatalf("response_format = %#v", format)
	}
}

func TestGeminiGenerateImageSendsExactBoundedEditInputs(t *testing.T) {
	first := testGeminiPNG(t)
	second := testGeminiJPEG(t)
	output := testGeminiJPEG(t)
	var inputs []geminiInteractionInput
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var body geminiImageInteractionRequest
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Errorf("decode request: %v", err)
		}
		inputs = body.Input
		writeGeminiImageResponse(t, writer, output, "image/jpeg")
	}))
	t.Cleanup(server.Close)

	provider := NewGeminiProvider("key", server.URL, "", "", 0, nil, nil)
	response, err := provider.GenerateImage(t.Context(), ImageGenerationRequest{
		Prompt: "Preserve the scene and change the caption",
		Model:  "gemini-3.1-flash-image",
		Count:  1,
		InputImages: []ImageGenerationInput{
			{Data: first, ContentType: "image/png", Filename: "/must/not/be/sent.png"},
			{Data: second, ContentType: "image/jpeg", Filename: "second.jpg"},
		},
	})
	if err != nil {
		t.Fatalf("GenerateImage() error = %v", err)
	}
	if len(inputs) != 3 || inputs[0].Type != "text" {
		t.Fatalf("inputs = %#v", inputs)
	}
	for index, want := range [][]byte{first, second} {
		got, decodeErr := base64.StdEncoding.DecodeString(inputs[index+1].Data)
		if decodeErr != nil || !bytes.Equal(got, want) {
			t.Fatalf("input %d bytes differ: err=%v", index+1, decodeErr)
		}
	}
	if len(response.Images) != 1 || !bytes.Equal(response.Images[0].Data, output) ||
		response.Images[0].MimeType != "image/jpeg" || response.Images[0].Ext != "jpg" {
		t.Fatalf("images = %#v", response.Images)
	}
}

func TestGeminiImageRequestFailsClosed(t *testing.T) {
	largePNG := make([]byte, geminiMaxInputBytes)
	copy(largePNG, []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'})
	oneBytePNG := append([]byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}, 1)
	tests := []struct {
		name string
		req  ImageGenerationRequest
		want string
	}{
		{
			name: "missing prompt",
			req:  ImageGenerationRequest{Model: "gemini-3.1-flash-image"},
			want: "prompt is required",
		},
		{
			name: "text model",
			req:  ImageGenerationRequest{Prompt: "x", Model: "gemini-3.1-flash"},
			want: "does not support native image",
		},
		{
			name: "multiple results",
			req:  ImageGenerationRequest{Prompt: "x", Model: "gemini-3.1-flash-image", Count: 2},
			want: "one result",
		},
		{
			name: "unsupported input",
			req: ImageGenerationRequest{
				Prompt:      "x",
				Model:       "gemini-3.1-flash-image",
				InputImages: []ImageGenerationInput{{Data: []byte("gif"), ContentType: "image/gif"}},
			},
			want: "PNG, JPEG, or WebP",
		},
		{
			name: "aggregate input bound",
			req: ImageGenerationRequest{
				Prompt: "x",
				Model:  "gemini-3.1-flash-image",
				InputImages: []ImageGenerationInput{
					{Data: largePNG, ContentType: "image/png"},
					{Data: oneBytePNG, ContentType: "image/png"},
				},
			},
			want: "input byte limit",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := buildGeminiImageRequest(test.req)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("buildGeminiImageRequest() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestGeminiGenerateImageRequiresAPIKeyBeforeNetwork(t *testing.T) {
	provider := NewGeminiProvider("", "https://example.invalid/v1beta", "", "", 0, nil, nil)
	_, err := provider.GenerateImage(t.Context(), ImageGenerationRequest{Prompt: "x"})
	if err == nil || !strings.Contains(err.Error(), "API key is not configured") {
		t.Fatalf("GenerateImage() error = %v", err)
	}
}

func TestGeminiImageResponseValidation(t *testing.T) {
	pngBytes := testGeminiPNG(t)
	encoded := base64.StdEncoding.EncodeToString(pngBytes)
	if _, err := decodeGeminiImage(encoded, "image/jpeg", len(pngBytes)); err == nil ||
		!strings.Contains(err.Error(), "MIME type did not match") {
		t.Fatalf("MIME mismatch error = %v", err)
	}
	if _, err := decodeGeminiImage(encoded, "image/png", len(pngBytes)-1); !errors.Is(
		err,
		errGeminiImageResponseTooLarge,
	) {
		t.Fatalf("oversize error = %v", err)
	}
	if _, err := decodeGeminiImage("not-base64", "image/png", 1024); err == nil {
		t.Fatal("malformed base64 was accepted")
	}
	if _, err := readBoundedGeminiImageResponse(strings.NewReader("12345"), 4); !errors.Is(
		err,
		errGeminiImageResponseTooLarge,
	) {
		t.Fatalf("response body error = %v", err)
	}
	_, err := decodeGeminiInteractionImages(geminiImageInteractionResponse{Status: "completed"})
	if err == nil || !strings.Contains(err.Error(), "returned no images") {
		t.Fatalf("empty response error = %v", err)
	}
}

func TestGeminiImageProviderNormalizesHTTPFailureWithoutLeakingKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		http.Error(writer, `{"error":{"message":"bad request"}}`, http.StatusBadRequest)
	}))
	t.Cleanup(server.Close)
	provider := NewGeminiProvider("do-not-leak", server.URL, "", "", 0, nil, nil)
	_, err := provider.GenerateImage(t.Context(), ImageGenerationRequest{Prompt: "x"})
	if err == nil || strings.Contains(err.Error(), "do-not-leak") {
		t.Fatalf("GenerateImage() error = %v", err)
	}
}

func writeGeminiImageResponse(t *testing.T, writer http.ResponseWriter, data []byte, mimeType string) {
	t.Helper()
	writer.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(writer).Encode(map[string]any{
		"status": "completed",
		"steps": []any{map[string]any{
			"type": "model_output",
			"content": []any{map[string]any{
				"type": "image", "mime_type": mimeType,
				"data": base64.StdEncoding.EncodeToString(data),
			}},
		}},
	}); err != nil {
		t.Errorf("encode response: %v", err)
	}
}

func testGeminiPNG(t *testing.T) []byte {
	t.Helper()
	var output bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{G: 255, A: 255})
	if err := png.Encode(&output, img); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}

func testGeminiJPEG(t *testing.T) []byte {
	t.Helper()
	var output bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 2, 2))
	img.Set(0, 0, color.RGBA{B: 255, A: 255})
	if err := jpeg.Encode(&output, img, nil); err != nil {
		t.Fatal(err)
	}
	return output.Bytes()
}
