package eval

import (
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/google/uuid"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

var errActiveToolFixture = errors.New("eval: active tool fixture unavailable")

const activeToolName = "eval_barrier"

// This is a read-only MCP fixture, not a steering queue or runtime authority.
// Only the owning driver can release a call; the model has no release tool.
type activeToolFixture struct {
	url     string
	server  *http.Server
	done    chan struct{}
	closing chan struct{}
	workers sync.WaitGroup

	mu       sync.Mutex
	barriers map[activeToolArgs]*activeToolBarrier
	inFlight int
	closed   bool
	failed   bool
	sequence int
	close    sync.Once
	closeErr error
}

type activeToolArgs struct {
	TrialID string `json:"trial_id"`
	Slot    int    `json:"slot"`
}

type activeToolObservation struct {
	TrialID         string `json:"trial_id"`
	SessionID       string `json:"session_id"`
	Slot            int    `json:"slot"`
	State           string `json:"state"`
	EnteredSequence int    `json:"entered_sequence"`
	ReleaseSequence int    `json:"release_sequence"`
	SettledSequence int    `json:"settled_sequence"`
}

type activeToolBarrier struct {
	observation activeToolObservation
	entered     chan struct{}
	released    chan struct{}
	settled     chan struct{}
}

func (*activeToolFixture) MarshalJSON() ([]byte, error) { return nil, errActiveToolFixture }

func newActiveToolFixture(address, namespace string) (*activeToolFixture, error) {
	host, port, err := net.SplitHostPort(address)
	number, portErr := strconv.Atoi(port)
	id, idErr := uuid.Parse(namespace)
	if err != nil || portErr != nil || strconv.Itoa(number) != port ||
		host != "127.0.0.1" && host != "::1" || address != net.JoinHostPort(host, port) ||
		number != 0 && (number < 1024 || number > 65535) || idErr != nil || id.Version() != 4 ||
		id.Variant() != uuid.RFC4122 || id.String() != namespace {
		return nil, errActiveToolFixture
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return nil, errActiveToolFixture
	}
	fixture := &activeToolFixture{
		url:  "http://" + listener.Addr().String() + "/eval/" + namespace + "/mcp",
		done: make(chan struct{}), closing: make(chan struct{}),
		barriers: make(map[activeToolArgs]*activeToolBarrier),
	}
	server := sdkmcp.NewServer(&sdkmcp.Implementation{Name: "mintclaw-eval-barrier", Version: "1"}, nil)
	sdkmcp.AddTool(server, &sdkmcp.Tool{
		Name: activeToolName, Description: "Read a synthetic fixture; wait until the test driver releases it.",
		Annotations: &sdkmcp.ToolAnnotations{ReadOnlyHint: true},
	}, fixture.call)
	handler := sdkmcp.NewStreamableHTTPHandler(func(*http.Request) *sdkmcp.Server { return server },
		&sdkmcp.StreamableHTTPOptions{Stateless: true, JSONResponse: true})
	fixture.server = &http.Server{
		ReadHeaderTimeout: 2 * time.Second, ReadTimeout: 2 * time.Second,
		WriteTimeout: 50 * time.Second, IdleTimeout: 2 * time.Second, MaxHeaderBytes: 8192,
		ErrorLog: log.New(io.Discard, "", 0),
		Handler: http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			if request.URL.Path != "/eval/"+namespace+"/mcp" || request.URL.RawPath != "" ||
				request.URL.RawQuery != "" ||
				request.URL.ForceQuery ||
				request.Host != listener.Addr().String() ||
				request.Header.Get("Origin") != "" {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			request.Body = http.MaxBytesReader(w, request.Body, 16384)
			handler.ServeHTTP(w, request)
		}),
	}
	go func() {
		defer close(fixture.done)
		if err := fixture.server.Serve(listener); !errors.Is(err, http.ErrServerClosed) {
			fixture.mu.Lock()
			fixture.failed = true
			fixture.mu.Unlock()
		}
	}()
	return fixture, nil
}

func (fixture *activeToolFixture) register(trialID, sessionID string, slot int) error {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	key := activeToolArgs{TrialID: trialID, Slot: slot}
	if !validID(trialID) || !validID(sessionID) || slot < 1 || slot > 2 || fixture.closed || fixture.failed ||
		len(fixture.barriers) >= 128 || fixture.barriers[key] != nil {
		return errActiveToolFixture
	}
	fixture.barriers[key] = &activeToolBarrier{
		observation: activeToolObservation{TrialID: trialID, SessionID: sessionID, Slot: slot, State: "registered"},
		entered:     make(chan struct{}), released: make(chan struct{}), settled: make(chan struct{}),
	}
	return nil
}

func (fixture *activeToolFixture) call(
	ctx context.Context, _ *sdkmcp.CallToolRequest, args activeToolArgs,
) (*sdkmcp.CallToolResult, any, error) {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	fixture.mu.Lock()
	barrier := fixture.barriers[args]
	if ctx.Err() != nil || fixture.closed || fixture.failed || barrier == nil ||
		barrier.observation.State != "registered" || fixture.inFlight >= 2 {
		fixture.mu.Unlock()
		return nil, nil, errActiveToolFixture
	}
	fixture.sequence++
	barrier.observation.State, barrier.observation.EnteredSequence = "active", fixture.sequence
	fixture.inFlight++
	fixture.workers.Add(1)
	close(barrier.entered)
	fixture.mu.Unlock()
	defer fixture.workers.Done()

	state := "released"
	select {
	case <-barrier.released:
	case <-ctx.Done():
		state = "canceled"
	case <-fixture.closing:
		state = "closed"
	}
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	if ctx.Err() != nil {
		state = "canceled"
	} else if fixture.closed || fixture.failed {
		state = "closed"
	}
	fixture.sequence++
	barrier.observation.State, barrier.observation.SettledSequence = state, fixture.sequence
	fixture.inFlight--
	close(barrier.settled)
	if state != "released" {
		return nil, nil, errActiveToolFixture
	}
	return &sdkmcp.CallToolResult{Content: []sdkmcp.Content{
		&sdkmcp.TextContent{Text: "Synthetic read completed; follow the latest user guidance."},
	}}, nil, nil
}

func (fixture *activeToolFixture) waitEntered(ctx context.Context, args activeToolArgs) error {
	fixture.mu.Lock()
	barrier := fixture.barriers[args]
	fixture.mu.Unlock()
	if barrier == nil || ctx.Err() != nil {
		return errActiveToolFixture
	}
	select {
	case <-barrier.entered:
		observation, err := fixture.snapshot(args)
		if err != nil || observation.State != "active" {
			return errActiveToolFixture
		}
		return nil
	case <-ctx.Done():
	case <-fixture.closing:
	}
	return errActiveToolFixture
}

func (fixture *activeToolFixture) release(args activeToolArgs) error {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	barrier := fixture.barriers[args]
	if fixture.closed || fixture.failed || barrier == nil || barrier.observation.State != "active" ||
		barrier.observation.ReleaseSequence != 0 {
		return errActiveToolFixture
	}
	fixture.sequence++
	barrier.observation.ReleaseSequence = fixture.sequence
	close(barrier.released)
	return nil
}

func (fixture *activeToolFixture) snapshot(args activeToolArgs) (activeToolObservation, error) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	barrier := fixture.barriers[args]
	if fixture.failed || barrier == nil {
		return activeToolObservation{}, errActiveToolFixture
	}
	return barrier.observation, nil
}

func (fixture *activeToolFixture) shutdown() error {
	fixture.close.Do(func() {
		fixture.mu.Lock()
		fixture.closed = true
		close(fixture.closing)
		fixture.mu.Unlock()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if fixture.server.Shutdown(ctx) != nil {
			fixture.closeErr = errActiveToolFixture
			_ = fixture.server.Close()
		}
		<-fixture.done
		fixture.workers.Wait()
		fixture.mu.Lock()
		if fixture.failed {
			fixture.closeErr = errActiveToolFixture
		}
		fixture.mu.Unlock()
	})
	return fixture.closeErr
}
