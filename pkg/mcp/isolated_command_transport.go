package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bogdanovich/mintclaw/pkg/isolation"
	"github.com/bogdanovich/mintclaw/pkg/logger"
)

var isolatedCommandTerminateDuration = 5 * time.Second

// isolatedCommandTransport mirrors the SDK command transport but routes
// process startup through pkg/isolation so Windows post-start hooks run too.
type isolatedCommandTransport struct {
	ServerName        string
	Command           *exec.Cmd
	TerminateDuration time.Duration
	cleanup           io.Closer
}

func (t *isolatedCommandTransport) Connect(ctx context.Context) (sdkmcp.Connection, error) {
	pipe, err := StartIsolatedCommand(ctx, t.ServerName, t.Command, t.TerminateDuration)
	if err != nil {
		return nil, err
	}
	t.cleanup = pipe
	return newIsolatedIOConn(pipe), nil
}

// IsolatedCommandConnection owns a command's stdio and complete descendant
// process tree. The neutral lifecycle interface is also used by trusted
// private sidecars that do not implement MCP.
type IsolatedCommandConnection interface {
	io.ReadWriteCloser
	Abort() error
}

// StartIsolatedCommand starts command in a host-specific process-tree
// boundary and returns its raw stdio connection. Protocol framing remains the
// caller's responsibility.
func StartIsolatedCommand(
	ctx context.Context,
	name string,
	command *exec.Cmd,
	terminateDuration time.Duration,
) (IsolatedCommandConnection, error) {
	if command == nil {
		return nil, errors.New("isolated command is required")
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stdout = io.NopCloser(stdout)
	stdin, err := command.StdinPipe()
	if err != nil {
		return nil, err
	}
	stderr, err := command.StderrPipe()
	if err != nil {
		return nil, err
	}
	processTree, err := prepareIsolatedCommandProcessTree(command)
	if err != nil {
		return nil, err
	}
	td := terminateDuration
	if td <= 0 {
		td = isolatedCommandTerminateDuration
	}
	pipe := &isolatedPipeRWC{
		stdout: stdout, stdin: stdin, terminateDuration: td,
		stopProcessTree: processTree.stop, abortProcessTree: processTree.abort,
	}
	if err := isolation.Start(command); err != nil {
		return nil, errors.Join(err, pipe.Close())
	}
	waitCh := make(chan error, 1)
	pipe.waitCh = waitCh
	go func() {
		err := command.Wait()
		fields := map[string]any{
			"server":  name,
			"command": command.Path,
			"pid":     command.Process.Pid,
		}
		if err != nil {
			fields["error"] = err.Error()
			logger.WarnCF("runtime", "Isolated stdio process exited with error", fields)
		} else {
			logger.InfoCF("runtime", "Isolated stdio process exited", fields)
		}
		waitCh <- err
	}()
	if err := processTree.started(); err != nil {
		_ = command.Process.Kill()
		return nil, errors.Join(err, pipe.Close())
	}
	logger.InfoCF("runtime", "Isolated stdio process started",
		map[string]any{
			"server":  name,
			"command": command.Path,
			"pid":     command.Process.Pid,
		})
	go logStdioProcessStderr(ctx, name, command.Path, stderr)
	return pipe, nil
}

type isolatedPipeRWC struct {
	closeMu           sync.Mutex
	stdinOnce         sync.Once
	closed            bool
	stopProcessTree   func(time.Duration) error
	abortProcessTree  func(time.Duration) error
	stdout            io.ReadCloser
	stdin             io.WriteCloser
	waitCh            <-chan error
	terminateDuration time.Duration
}

func (s *isolatedPipeRWC) Read(p []byte) (n int, err error) {
	return s.stdout.Read(p)
}

func (s *isolatedPipeRWC) Write(p []byte) (n int, err error) {
	return s.stdin.Write(p)
}

func (s *isolatedPipeRWC) Close() error {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	if s.closed {
		return nil
	}
	// Closing stdin gives a cooperative child process its first opportunity to
	// stop. Process-tree termination then proves that wrappers and browser
	// descendants sharing the owned process group are gone before Close
	// succeeds and an exclusive profile lease may be released.
	s.stdinOnce.Do(func() { _ = s.stdin.Close() })
	if err := s.stopProcessTree(s.terminateDuration); err != nil {
		return err
	}
	if s.waitCh == nil {
		s.closed = true
		return nil
	}
	timer := time.NewTimer(s.terminateDuration)
	defer timer.Stop()
	select {
	case <-s.waitCh:
		s.closed = true
		return nil
	case <-timer.C:
		return fmt.Errorf("browser driver process was not reaped after tree termination")
	}
}

// Abort kills the isolated server process tree before closing its protocol
// input. This is intentionally distinct from Close: some attached-resource
// servers perform destructive remote cleanup when they observe EOF or a
// cooperative termination signal.
func (s *isolatedPipeRWC) Abort() error {
	s.closeMu.Lock()
	defer s.closeMu.Unlock()
	if s.closed {
		return nil
	}
	if s.abortProcessTree == nil {
		return errors.New("abrupt process-tree cleanup is unavailable")
	}
	if err := s.abortProcessTree(s.terminateDuration); err != nil {
		return err
	}
	s.stdinOnce.Do(func() { _ = s.stdin.Close() })
	if s.waitCh == nil {
		s.closed = true
		return nil
	}
	timer := time.NewTimer(s.terminateDuration)
	defer timer.Stop()
	select {
	case <-s.waitCh:
		s.closed = true
		return nil
	case <-timer.C:
		return errors.New("process was not reaped after abort")
	}
}

func logStdioProcessStderr(ctx context.Context, serverName, command string, stderr io.Reader) {
	scanner := bufio.NewScanner(stderr)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		logger.InfoCF("runtime", "Isolated stdio stderr",
			map[string]any{
				"server":  serverName,
				"command": command,
				"line":    line,
			})
	}
	if err := scanner.Err(); err != nil && ctx.Err() == nil {
		logger.WarnCF("runtime", "Isolated stdio stderr reader failed",
			map[string]any{
				"server":  serverName,
				"command": command,
				"error":   err.Error(),
			})
	}
}

type isolatedIOConn struct {
	writeMu   sync.Mutex
	rwc       io.ReadWriteCloser
	incoming  <-chan isolatedMsgOrErr
	queue     []jsonrpc.Message
	closeOnce sync.Once
	closed    chan struct{}
	closeErr  error
}

type isolatedMsgOrErr struct {
	msg json.RawMessage
	err error
}

func newIsolatedIOConn(rwc io.ReadWriteCloser) *isolatedIOConn {
	incoming := make(chan isolatedMsgOrErr)
	closed := make(chan struct{})
	go func() {
		dec := json.NewDecoder(rwc)
		for {
			var raw json.RawMessage
			err := dec.Decode(&raw)
			if err == nil {
				var tr [1]byte
				if n, readErr := dec.Buffered().Read(tr[:]); n > 0 {
					if tr[0] != '\n' && tr[0] != '\r' {
						err = fmt.Errorf("invalid trailing data at the end of stream")
					}
				} else if readErr != nil && readErr != io.EOF {
					err = readErr
				}
			}
			select {
			case incoming <- isolatedMsgOrErr{msg: raw, err: err}:
			case <-closed:
				return
			}
			if err != nil {
				return
			}
		}
	}()
	return &isolatedIOConn{rwc: rwc, incoming: incoming, closed: closed}
}

func (c *isolatedIOConn) SessionID() string { return "" }

func (c *isolatedIOConn) Read(ctx context.Context) (jsonrpc.Message, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	default:
	}
	if len(c.queue) > 0 {
		next := c.queue[0]
		c.queue = c.queue[1:]
		return next, nil
	}
	var raw json.RawMessage
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case v := <-c.incoming:
		if v.err != nil {
			return nil, v.err
		}
		raw = v.msg
	case <-c.closed:
		return nil, io.EOF
	}
	msgs, err := readIsolatedBatch(raw)
	if err != nil {
		return nil, err
	}
	c.queue = msgs[1:]
	return msgs[0], nil
}

func readIsolatedBatch(data []byte) ([]jsonrpc.Message, error) {
	var rawBatch []json.RawMessage
	if err := json.Unmarshal(data, &rawBatch); err == nil {
		if len(rawBatch) == 0 {
			return nil, fmt.Errorf("empty batch")
		}
		msgs := make([]jsonrpc.Message, 0, len(rawBatch))
		for _, raw := range rawBatch {
			msg, err := jsonrpc.DecodeMessage(raw)
			if err != nil {
				return nil, err
			}
			msgs = append(msgs, msg)
		}
		return msgs, nil
	}
	msg, err := jsonrpc.DecodeMessage(data)
	if err != nil {
		return nil, err
	}
	return []jsonrpc.Message{msg}, nil
}

func (c *isolatedIOConn) Write(ctx context.Context, msg jsonrpc.Message) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	data, err := jsonrpc.EncodeMessage(msg)
	if err != nil {
		return fmt.Errorf("marshaling message: %w", err)
	}
	data = append(data, '\n')
	_, err = c.rwc.Write(data)
	return err
}

func (c *isolatedIOConn) Close() error {
	c.closeOnce.Do(func() {
		c.closeErr = c.rwc.Close()
		close(c.closed)
	})
	return c.closeErr
}

var (
	_ sdkmcp.Transport  = (*isolatedCommandTransport)(nil)
	_ sdkmcp.Connection = (*isolatedIOConn)(nil)
)
