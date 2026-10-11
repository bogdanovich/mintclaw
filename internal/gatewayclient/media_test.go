package gatewayclient

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

func mediaControl(data []byte) MediaEvidence {
	sum := sha256.Sum256(data)
	return MediaEvidence{ContentType: "application/pdf", SHA256: hex.EncodeToString(sum[:]), Size: int64(len(data))}
}

func TestDownloadMediaUsesBoundHeaderAuthAndBypassesProxy(t *testing.T) {
	var proxyCalls, targetCalls atomic.Int32
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		proxyCalls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer proxy.Close()
	t.Setenv("HTTP_PROXY", proxy.URL)
	t.Setenv("http_proxy", proxy.URL)
	t.Setenv("NO_PROXY", "")
	data := []byte("%PDF-1.7\nprivate-file-only-control")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		targetCalls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != "/mintclaw/media/artifact-control" ||
			r.URL.RawQuery != "" || r.Header.Get("Authorization") != "Bearer test-token" ||
			r.Header.Get("Accept") != "application/pdf" || r.Header.Get("Accept-Encoding") != "" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/pdf")
		w.Header().Set("Content-Disposition", "attachment; filename=private-ignored.pdf")
		_, _ = w.Write(data)
	}))
	defer server.Close()
	actual, err := DownloadMedia(t.Context(), testConfig(t, server.URL), "media://artifact-control", mediaControl(data))
	defer clear(actual)
	if err != nil || !bytes.Equal(actual, data) || targetCalls.Load() != 1 || proxyCalls.Load() != 0 {
		t.Fatal("authenticated exact bytes were unavailable, replayed, or sent through a proxy", err)
	}
}

func TestDownloadMediaRefusesUnadmittedMetadataBeforeNetwork(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	cfg := testConfig(t, server.URL)
	expected := mediaControl([]byte("control"))
	for _, ref := range []string{
		"https://private.example/file", "media://../file", "media://id?token=private", "media://id?",
		"media://id#fragment", "media://user@id", "media://id:80", "media://id/path", "media://..", "media://%2fid",
	} {
		_, err := DownloadMedia(t.Context(), cfg, ref, expected)
		if !errors.Is(err, ErrMediaUnavailable) || strings.Contains(err.Error(), "private") {
			t.Fatal("unadmitted media reference became network authority or echoed input")
		}
	}
	for _, mode := range []string{"size", "digest", "type"} {
		bad := expected
		switch mode {
		case "size":
			bad.Size = MaxMediaBytes + 1
		case "digest":
			bad.SHA256 = "private-invalid-digest"
		case "type":
			bad.ContentType = "application/pdf; private=value"
		}
		if _, err := DownloadMedia(t.Context(), cfg, "media://id", bad); !errors.Is(err, ErrMediaUnavailable) {
			t.Fatal("unadmitted evidence was fetched", err)
		}
	}
	if calls.Load() != 0 {
		t.Fatal("admission refusal contacted the target")
	}
}

func TestDownloadMediaRejectsMismatchAndNeverRetainsPrivateFailure(t *testing.T) {
	data := []byte("%PDF-control-private-bytes")
	for _, mode := range []string{"type", "digest", "length", "stream_overflow", "encoded", "unauthorized"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.Header().Set("Content-Type", "application/pdf")
				switch mode {
				case "type":
					w.Header().Set("Content-Type", "text/plain")
				case "encoded":
					w.Header().Set("Content-Encoding", "gzip")
				case "unauthorized":
					w.WriteHeader(http.StatusUnauthorized)
				case "stream_overflow":
					w.(http.Flusher).Flush()
				}
				if mode == "digest" {
					_, _ = w.Write(bytes.Repeat([]byte("x"), len(data)))
				} else {
					_, _ = w.Write(data)
				}
			}))
			defer server.Close()
			expected := mediaControl(data)
			if mode == "length" || mode == "stream_overflow" {
				expected.Size--
			}
			actual, err := DownloadMedia(t.Context(), testConfig(t, server.URL), "media://id", expected)
			want := ErrMediaMismatch
			if mode == "unauthorized" {
				want = ErrMediaUnavailable
			}
			if !errors.Is(err, want) || actual != nil || calls.Load() != 1 ||
				strings.Contains(err.Error(), "private") || strings.Contains(err.Error(), "test-token") {
				t.Fatal("bad/unknown media became retained bytes, replay, or raw diagnostics", err)
			}
		})
	}
}

func TestDownloadMediaNeverFollowsRedirectOrForwardsCredentials(t *testing.T) {
	var redirected atomic.Int32
	other := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		redirected.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer other.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+"/private-location", http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	data, err := DownloadMedia(t.Context(), testConfig(t, server.URL), "media://id", mediaControl([]byte("control")))
	if !errors.Is(err, ErrMediaUnavailable) || data != nil || redirected.Load() != 0 ||
		strings.Contains(err.Error(), "private-location") {
		t.Fatal("redirect location was followed, echoed, or given credentials", err)
	}
}

func TestDownloadMediaCancellationDoesNotReturnPartialBytesOrRetry(t *testing.T) {
	started := make(chan struct{})
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/pdf")
		w.(http.Flusher).Flush()
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		select {
		case <-started:
			cancel()
		case <-ctx.Done():
		}
	}()
	data, err := DownloadMedia(ctx, testConfig(t, server.URL), "media://id", mediaControl([]byte("control")))
	cancel()
	<-finished
	if !errors.Is(err, ErrMediaUnavailable) || data != nil || calls.Load() != 1 {
		t.Fatal("cancellation returned partial evidence or replayed a request", err)
	}
}
