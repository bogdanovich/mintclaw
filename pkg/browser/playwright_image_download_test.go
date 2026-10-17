package browser

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/config"
)

func TestBrokerDownloadNotDispatchedPreservesSessionWithoutReplay(t *testing.T) {
	for _, test := range []struct {
		name      string
		err       error
		preserved bool
	}{
		{"preflight", ErrDownloadNotDispatched, true},
		{"transport", errors.Join(ErrDownloadNotDispatched, ErrWorkerUnavailable), false},
		{"driver", errors.Join(ErrDownloadNotDispatched, ErrDriverRejected), false},
		{"protocol", errors.Join(ErrDownloadNotDispatched, ErrDriverIncompatible), false},
		{"canceled", errors.Join(ErrDownloadNotDispatched, context.Canceled), false},
		{"deadline", errors.Join(ErrDownloadNotDispatched, context.DeadlineExceeded), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			broker, worker, session := openActionTestBroker(t, NewMemoryStore())
			observed, err := broker.Observe(t.Context(), testOwner(), session.ID, session.TabID)
			if err != nil {
				t.Fatal(err)
			}
			prepared, err := broker.PrepareAction(t.Context(), PrepareActionRequest{
				Owner: testOwner(), RequestID: "download_preflight", SessionID: session.ID, TabID: session.TabID,
				SnapshotID: observed.SnapshotID, SnapshotGeneration: observed.SnapshotGeneration,
				Action: Action{Kind: ActionDownload, Ref: onlyVisibleRef(t, observed.Snapshot)},
			})
			if err != nil {
				t.Fatal(err)
			}
			worker.executeErr = test.err
			sink := func(context.Context, PreparedAction, DriverDownload) (json.RawMessage, error) {
				t.Fatal("preflight failure reached artifact retention")
				return nil, nil
			}
			invocation, err := broker.ExecuteActionWithDownloadSink(
				t.Context(),
				testOwner(),
				prepared.Action.ID,
				&prepared.Approval,
				sink,
			)
			if err != nil {
				t.Fatal(err)
			}
			status, err := broker.Status(t.Context(), testOwner(), session.ID)
			if err != nil {
				t.Fatal(err)
			}
			if !test.preserved {
				if invocation.State != InvocationUnknown || status.State != SessionLost || worker.closed != 1 {
					t.Fatalf("uncertain download = %#v; session = %#v", invocation, status)
				}
				return
			}
			if invocation.State != InvocationFailed || invocation.SafeFailure != "download_not_dispatched" ||
				invocation.Diagnostic == nil || status.State != SessionReady || worker.closed != 0 {
				t.Fatalf("safe download failure = %#v; session = %#v", invocation, status)
			}
			replayed, err := broker.ExecuteActionWithDownloadSink(
				t.Context(),
				testOwner(),
				prepared.Action.ID,
				&prepared.Approval,
				sink,
			)
			if err != nil || replayed.State != InvocationFailed || len(worker.actions) != 1 {
				t.Fatalf("replay = %#v, %v; driver calls = %d", replayed, err, len(worker.actions))
			}
			if _, err = broker.Observe(t.Context(), testOwner(), session.ID, session.TabID); err != nil {
				t.Fatalf("same-session observation failed: %v", err)
			}
		})
	}
}

func TestPlaywrightImageDownloadRealBrowser(t *testing.T) {
	if os.Getenv("MINTCLAW_BROWSER_REAL_DRIVER") != "1" {
		t.Skip("set MINTCLAW_BROWSER_REAL_DRIVER=1 for real image capture")
	}
	payload, err := base64.StdEncoding.DecodeString(
		"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jRZkAAAAASUVORK5CYII=",
	)
	if err != nil {
		t.Fatal(err)
	}
	var authenticated atomic.Int64
	var svgScriptRequests atomic.Int64
	svg := []byte(
		`<svg xmlns="http://www.w3.org/2000/svg" width="24" height="24"><text>Photo: 你好</text><script>fetch('/svg-script-executed')</script></svg>`,
	)
	var deniedRequests atomic.Int64
	var denyRedirect atomic.Bool
	denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		deniedRequests.Add(1)
		_, _ = w.Write(payload)
	}))
	defer denied.Close()
	uploaded := make(chan []byte, 1)
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/photo.svg":
			w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
			_, _ = w.Write(svg)
		case "/svg-script-executed":
			svgScriptRequests.Add(1)
			_, _ = io.WriteString(w, "unexpected SVG execution")
		case "/denied.png":
			if denyRedirect.Load() {
				http.Redirect(w, r, denied.URL+"/image.png", http.StatusFound)
			} else {
				w.Header().Set("Content-Type", "image/png")
				_, _ = w.Write(payload)
			}
		case "/image.png", "/redirect.png":
			cookie, cookieErr := r.Cookie("image-auth")
			if cookieErr != nil || cookie.Value != "fixture" {
				http.Error(w, "sign in", http.StatusUnauthorized)
				return
			}
			authenticated.Add(1)
			if r.URL.Path == "/redirect.png" {
				http.Redirect(w, r, "/image.png", http.StatusFound)
				return
			}
			w.Header().Set("Content-Type", "image/png")
			_, _ = w.Write(payload)
		case "/upload":
			body, readErr := io.ReadAll(io.LimitReader(r.Body, 4096))
			if readErr != nil {
				http.Error(w, "read failed", 400)
				return
			}
			uploaded <- body
			_, _ = io.WriteString(w, "uploaded")
		case "/attachment":
			w.Header().Set("Content-Type", "text/plain")
			w.Header().Set("Content-Disposition", `attachment; filename="fixture.txt"`)
			_, _ = io.WriteString(w, "attachment fixture")
		default:
			w.Header().Set("Content-Type", "text/html")
			http.SetCookie(w, &http.Cookie{Name: "image-auth", Value: "fixture", Path: "/", HttpOnly: true})
			_, _ = io.WriteString(w, `<title>Original image fixture</title>
<img alt="Product photo" src="/redirect.png"><button>Unsupported target</button>
<img alt="SVG photo" src="/photo.svg">
<img alt="Denied photo" src="/denied.png">
<img alt="Inline photo" src="data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7">
<a href="/attachment">Attachment fixture</a>
<input type="file" aria-label="Upload fixture" onchange="document.querySelector('#upload-state').textContent='UPLOAD_SELECTED_'+this.files.length">
<button onclick="fetch('/upload',{method:'POST',body:document.querySelector('input[type=file]').files[0]}).then(()=>document.querySelector('#upload-state').textContent='UPLOAD_RECEIVED').catch(()=>document.querySelector('#upload-state').textContent='UPLOAD_FAILED')">Send test upload</button>
<output id="upload-state">UPLOAD_NOT_SELECTED</output>`)
		}
	}))
	defer fixture.Close()
	fixtureOrigin := "http://example.com:" + strconv.Itoa(fixture.Listener.Addr().(*net.TCPAddr).Port)
	root := runtimeAdmittedBrowserConfig(t, false)
	target := root.Tools.Browser.Targets[config.BrowserDefaultTarget]
	profile := target.Profiles[config.BrowserDefaultProfile]
	// Full Chrome makes browser-level background requests, unlike the bundled
	// headless shell. Match the other real-driver fixtures: public_web still
	// denies the private redirect, and every permitted dial is forced into our
	// owned listener below, so no request can reach an external service.
	profile.NetworkMode, profile.AllowedOrigins = config.BrowserNetworkPublicWeb, nil
	profile.CapabilityMode, profile.ApprovalMode = config.BrowserCapabilityFullAccess, "model_requested"
	profile.DryRun, profile.AllowApprovedActions = false, true
	target.Profiles[config.BrowserDefaultProfile] = profile
	root.Tools.Browser.Targets[config.BrowserDefaultTarget] = target
	configureRealPlaywrightLibraryDriver(t, root)
	factory, err := NewPlaywrightWorkerFactory(root)
	if err != nil {
		t.Fatal(err)
	}
	factory.proxyLookupIP = func(context.Context, string, string) ([]net.IP, error) {
		return []net.IP{net.ParseIP("8.8.8.8")}, nil
	}
	factory.proxyDial = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, fixture.Listener.Addr().String())
	}
	ctx, cancel := context.WithTimeout(t.Context(), 45*time.Second)
	defer cancel()
	opened, err := factory.Open(ctx, WorkerOpenRequest{
		SessionID: "image_fixture_session", Target: "gateway", Profile: "managed",
		DryRun: profile.DryRun, ProfileRevision: profile.Revision, Limits: config.BrowserLimitsConfig{},
	})
	if err != nil {
		t.Fatal(err)
	}
	worker := opened.Owner.(*playwrightWorker)
	t.Cleanup(func() { _ = worker.Close(context.Background()) })
	if err = worker.Execute(ctx, DriverAction{Kind: DriverNavigate, URL: fixtureOrigin}); err != nil {
		diagnostics, diagnosticErr := worker.Diagnostics(ctx, []DiagnosticCategory{DiagnosticFailedRequests})
		t.Fatalf(
			"fixture navigation: %v; proxy denials=%d; diagnostics=%+v, %v",
			err,
			worker.networkProxy.Denials(),
			diagnostics,
			diagnosticErr,
		)
	}
	observe := func() DriverObservation {
		t.Helper()
		observation, observeErr := worker.Observe(ctx)
		if observeErr != nil {
			t.Fatal(observeErr)
		}
		return observation
	}
	observation := observe()
	unsupported := mustSnapshotRef(t, observation.Snapshot, `button "Unsupported target" \[ref=([a-z0-9]*e[0-9]+)\]`)
	if _, err = worker.Download(
		ctx,
		DriverAction{Kind: DriverDownloadAction, Target: unsupported},
		1024,
	); !DownloadNotDispatched(
		err,
	) {
		t.Fatalf("unsupported target = %v", err)
	}
	observation = observe()
	inline := mustSnapshotRef(t, observation.Snapshot, `img "Inline photo" \[ref=([a-z0-9]*e[0-9]+)\]`)
	if _, err = worker.Download(
		ctx,
		DriverAction{Kind: DriverDownloadAction, Target: inline},
		1024,
	); !DownloadNotDispatched(
		err,
	) {
		t.Fatalf("inline source must reject before dispatch: %v", err)
	}
	observation = observe()
	deniedImage := mustSnapshotRef(t, observation.Snapshot, `img "Denied photo" \[ref=([a-z0-9]*e[0-9]+)\]`)
	denyRedirect.Store(true)
	if _, err = worker.Download(
		ctx,
		DriverAction{Kind: DriverDownloadAction, Target: deniedImage},
		1024,
	); !errors.Is(
		err,
		ErrDenied,
	) {
		t.Fatalf("forbidden redirect = %v", err)
	}
	if deniedRequests.Load() != 0 || worker.lost {
		t.Fatal("resource capture bypassed network policy or retired healthy worker")
	}
	observation = observe()
	image := mustSnapshotRef(t, observation.Snapshot, `img "Product photo" \[ref=([a-z0-9]*e[0-9]+)\]`)
	_, limitErr := worker.Download(ctx, DriverAction{Kind: DriverDownloadAction, Target: image}, 1)
	var artifactFailure *DownloadArtifactError
	if !errors.As(limitErr, &artifactFailure) {
		t.Fatalf("image byte limit = %v", limitErr)
	}
	observation = observe()
	image = mustSnapshotRef(t, observation.Snapshot, `img "Product photo" \[ref=([a-z0-9]*e[0-9]+)\]`)
	captured, err := worker.Download(ctx, DriverAction{Kind: DriverDownloadAction, Target: image}, 1024)
	if err != nil {
		t.Fatalf("image capture: %v", err)
	}
	digest := sha256.Sum256(payload)
	content, err := os.ReadFile(captured.Path)
	if err != nil || !bytes.Equal(content, payload) || captured.ContentType != "image/png" ||
		captured.SHA256 != hex.EncodeToString(
			digest[:],
		) || captured.Size != int64(len(payload)) || authenticated.Load() < 4 {
		t.Fatalf(
			"original image = %+v; bytes match=%t, auth requests=%d, err=%v",
			captured,
			bytes.Equal(content, payload),
			authenticated.Load(),
			err,
		)
	}
	observation = observe()
	if observation.URL != fixtureOrigin+"/" || observation.Title != "Original image fixture" {
		t.Fatalf("source page changed: %+v", observation)
	}
	svgRef := mustSnapshotRef(t, observation.Snapshot, `img "SVG photo" \[ref=([a-z0-9]*e[0-9]+)\]`)
	svgDownload, err := worker.Download(ctx, DriverAction{Kind: DriverDownloadAction, Target: svgRef}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	svgContent, err := os.ReadFile(svgDownload.Path)
	if err != nil || !bytes.Equal(svgContent, svg) || svgDownload.Filename != "image.svg" ||
		svgDownload.ContentType != "image/svg+xml" || svgScriptRequests.Load() != 0 {
		t.Fatalf(
			"SVG original-byte capture failed: %+v, script requests=%d, error=%v",
			svgDownload,
			svgScriptRequests.Load(),
			err,
		)
	}
	observation = observe()
	chooser := mustSnapshotRef(t, observation.Snapshot, `button "Upload fixture" \[ref=([a-z0-9]*e[0-9]+)\]`)
	identity, err := worker.NavigationIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = worker.UploadAfterNavigationCheck(ctx, identity, DriverAction{
		Kind: DriverUpload, Target: chooser, Value: captured.Path,
		ArtifactFilename: captured.Filename, ArtifactContentType: captured.ContentType,
		ArtifactBytes: captured.Size, ArtifactSHA256: captured.SHA256,
	}); err != nil {
		t.Fatal(err)
	}
	// The site reads the file in a later action, after the assignment RPC and
	// a fresh observation have completed. A filename/size check cannot prove it.
	observation = observe()
	submit := mustSnapshotRef(t, observation.Snapshot, `button "Send test upload" \[ref=([a-z0-9]*e[0-9]+)\]`)
	identity, err = worker.NavigationIdentity(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = worker.ExecuteAfterNavigationCheck(
		ctx,
		identity,
		DriverAction{Kind: DriverClick, Target: submit},
	); err != nil {
		t.Fatal(err)
	}
	select {
	case received := <-uploaded:
		if !bytes.Equal(received, payload) {
			t.Fatalf("uploaded bytes differ: %d", len(received))
		}
	case <-ctx.Done():
		t.Fatal("upload was not received")
	case <-time.After(5 * time.Second):
		t.Fatal("upload was not received within fixture deadline")
	}
	observation = observe()
	attachment := mustSnapshotRef(t, observation.Snapshot, `link "Attachment fixture" \[ref=([a-z0-9]*e[0-9]+)\]`)
	file, err := worker.Download(ctx, DriverAction{Kind: DriverDownloadAction, Target: attachment}, 1024)
	if err != nil {
		t.Fatal(err)
	}
	content, err = os.ReadFile(file.Path)
	if err != nil || string(content) != "attachment fixture" || file.ContentType != "text/plain" ||
		file.Filename != "fixture.txt" {
		t.Fatalf("attachment = %+v, %q, %v", file, content, err)
	}
}
