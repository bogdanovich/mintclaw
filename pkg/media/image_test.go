package media

import "testing"

func TestDetectSupportedImageContentTypeUsesCanonicalSignatures(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{name: "png", data: []byte("\x89PNG\r\n\x1a\n"), want: "image/png"},
		{name: "jpeg", data: []byte("\xff\xd8\xff"), want: "image/jpeg"},
		{name: "gif87a", data: []byte("GIF87a"), want: "image/gif"},
		{name: "gif89a", data: []byte("GIF89a"), want: "image/gif"},
		{name: "webp", data: []byte("RIFF\x00\x00\x00\x00WEBP"), want: "image/webp"},
		{name: "GIF text", data: []byte("GIF report: no image")},
		{name: "WEBP text", data: []byte("12345678WEBP report")},
		{name: "RIFF text", data: []byte("RIFF report WEBP")},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := DetectSupportedImageContentType(test.data); got != test.want {
				t.Fatalf("DetectSupportedImageContentType() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestIsSupportedImageContentTypeAcceptsOnlyCanonicalProviderImages(t *testing.T) {
	for _, contentType := range []string{"image/png", "image/jpeg", "image/gif", "image/webp"} {
		if !IsSupportedImageContentType(contentType) {
			t.Fatalf("IsSupportedImageContentType(%q) = false", contentType)
		}
	}
	for _, contentType := range []string{"image/svg+xml", "image/png; charset=binary", "application/octet-stream", ""} {
		if IsSupportedImageContentType(contentType) {
			t.Fatalf("IsSupportedImageContentType(%q) = true", contentType)
		}
	}
}
