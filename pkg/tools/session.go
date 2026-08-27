package tools

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/google/uuid"

	toolshared "github.com/bogdanovich/mintclaw/pkg/tools/shared"
)

const maxOutputBufferSize = 1 * 1024 * 1024 // 1MB

const outputTruncateMarker = "\n... [output truncated, exceeded 1MB]\n"

// PtyKeyMode represents arrow key encoding mode for PTY sessions.
// Programs send smkx/rmkx sequences to switch between CSI and SS3 modes.
type PtyKeyMode uint8

const (
	PtyKeyModeCSI PtyKeyMode = iota // triggered by rmkx (\x1b[?1l)
	PtyKeyModeSS3                   // triggered by smkx (\x1b[?1h)
)

const PtyKeyModeNotFound PtyKeyMode = 255

var (
	ErrSessionNotFound = errors.New("session not found")
	ErrSessionDone     = errors.New("session already completed")
	ErrPTYNotSupported = errors.New("PTY is not supported on this platform")
	ErrNoStdin         = errors.New("no stdin available")
	ErrSessionWait     = errors.New("session completion is unavailable")
)

type ProcessSession struct {
	mu              sync.Mutex
	ID              string
	PID             int
	Command         string
	PTY             bool
	Background      bool
	StartTime       int64
	ExitCode        int
	Status          string
	stdinWriter     io.Writer
	stdoutPipe      io.Reader
	outputBuffer    *bytes.Buffer
	outputTruncated bool
	ptyMaster       *os.File
	completion      chan struct{}
	waitErr         error
	completionOnce  sync.Once
	terminate       func(int) error

	// ptyKeyMode tracks arrow key encoding mode (CSI vs SS3)
	ptyKeyMode PtyKeyMode
}

func (s *ProcessSession) IsDone() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Status == "done" || s.Status == "exited" || s.Status == "error"
}

func (s *ProcessSession) GetPtyKeyMode() PtyKeyMode {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ptyKeyMode
}

func (s *ProcessSession) SetPtyKeyMode(mode PtyKeyMode) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ptyKeyMode = mode
}

func (s *ProcessSession) GetStatus() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.Status
}

func (s *ProcessSession) GetExitCode() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ExitCode
}

func (s *ProcessSession) killProcess() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.Status != "running" {
		return ErrSessionDone
	}

	pid := s.PID
	if pid <= 0 {
		return ErrSessionNotFound
	}

	terminate := s.terminate
	if terminate == nil {
		terminate = killProcessGroup
	}
	if err := terminate(pid); err != nil {
		return err
	}

	return nil
}

func (s *ProcessSession) Kill() error {
	return s.killProcess()
}

func (s *ProcessSession) complete(exitCode int, waitErr error) {
	s.mu.Lock()
	s.ExitCode = exitCode
	s.waitErr = normalizeSessionWaitError(waitErr)
	if s.waitErr != nil {
		s.Status = "error"
	} else {
		s.Status = "done"
	}
	s.mu.Unlock()
	if s.completion != nil {
		s.completionOnce.Do(func() {
			close(s.completion)
		})
	}
}

func normalizeSessionWaitError(err error) error {
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) {
		return nil
	}
	return err
}

func (s *ProcessSession) hasCompletion() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.completion != nil
}

func (s *ProcessSession) waitForCompletion() error {
	s.mu.Lock()
	completion := s.completion
	status := s.Status
	waitErr := s.waitErr
	s.mu.Unlock()
	if completion == nil {
		if status == "done" || status == "exited" || status == "error" {
			return waitErr
		}
		return ErrSessionWait
	}
	<-completion
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.waitErr
}

func (s *ProcessSession) Write(data string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.Status != "running" {
		return ErrSessionDone
	}

	var writer io.Writer
	if s.PTY && s.ptyMaster != nil {
		writer = s.ptyMaster
	} else if s.stdinWriter != nil {
		writer = s.stdinWriter
	} else {
		return ErrNoStdin
	}

	_, err := writer.Write([]byte(data))
	return err
}

func (s *ProcessSession) Read() string {
	output, _ := s.ReadObservation()
	return output
}

// ReadObservation drains buffered output and reports whether the bounded
// process buffer has truncated any output in this session.
func (s *ProcessSession) ReadObservation() (string, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.outputBuffer.Len() == 0 {
		return "", s.outputTruncated
	}

	data := s.outputBuffer.String()
	s.outputBuffer.Reset()
	return data, s.outputTruncated
}

func (s *ProcessSession) ToSessionInfo() toolshared.SessionInfo {
	s.mu.Lock()
	defer s.mu.Unlock()

	return toolshared.SessionInfo{
		ID:        s.ID,
		Command:   s.Command,
		Status:    s.Status,
		PID:       s.PID,
		StartedAt: s.StartTime,
	}
}

type SessionManager struct {
	mu              sync.RWMutex
	sessions        map[string]*ProcessSession
	closing         bool
	admissionWG     sync.WaitGroup
	admissionErrors []error
	stopCh          chan struct{}
	stopOnce        sync.Once
	closeOnce       sync.Once
	closeDone       chan struct{}
	closeErr        error
}

type sessionAdmission struct {
	manager *SessionManager
	once    sync.Once
}

func NewSessionManager() *SessionManager {
	sm := &SessionManager{
		sessions:  make(map[string]*ProcessSession),
		stopCh:    make(chan struct{}),
		closeDone: make(chan struct{}),
	}

	// Start cleaner goroutine - runs every 5 minutes, cleans up sessions done for >30 minutes
	go func() {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-sm.stopCh:
				return
			case <-ticker.C:
				sm.cleanupOldSessions()
			}
		}
	}()

	return sm
}

// Stop shuts down the background cleanup goroutine. Safe to call multiple
// times from concurrent goroutines. After Stop returns, the SessionManager
// is still usable — only the cleanup goroutine is terminated.
func (sm *SessionManager) Stop() {
	sm.stopOnce.Do(func() {
		close(sm.stopCh)
	})
}

// cleanupOldSessions removes sessions that are done and older than 30 minutes
func (sm *SessionManager) cleanupOldSessions() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	cutoff := time.Now().Add(-30 * time.Minute)
	for id, session := range sm.sessions {
		if session.IsDone() && session.StartTime < cutoff.Unix() {
			delete(sm.sessions, id)
		}
	}
}

func (sm *SessionManager) Add(session *ProcessSession) bool {
	admission, ok := sm.beginAdmission()
	if !ok {
		return false
	}
	admitted := admission.admit(session)
	admission.finish(nil)
	return admitted
}

// beginAdmission reserves startup work before a process is created. Close
// seals this gate and waits for every reservation to commit or clean up.
func (sm *SessionManager) beginAdmission() (*sessionAdmission, bool) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	if sm.closing {
		return nil, false
	}
	sm.admissionWG.Add(1)
	return &sessionAdmission{manager: sm}, true
}

func (a *sessionAdmission) admit(session *ProcessSession) bool {
	if a == nil || a.manager == nil {
		return false
	}
	a.manager.mu.Lock()
	defer a.manager.mu.Unlock()
	if a.manager.closing {
		return false
	}
	a.manager.sessions[session.ID] = session
	return true
}

func (a *sessionAdmission) finish(err error) {
	if a == nil || a.manager == nil {
		return
	}
	a.once.Do(func() {
		if err != nil {
			a.manager.mu.Lock()
			a.manager.admissionErrors = append(a.manager.admissionErrors, err)
			a.manager.mu.Unlock()
		}
		a.manager.admissionWG.Done()
	})
}

// Close rejects new sessions, terminates admitted processes, and stops cleanup.
func (sm *SessionManager) Close() error {
	sm.closeOnce.Do(func() {
		sm.mu.Lock()
		sm.closing = true
		sm.mu.Unlock()

		sm.Stop()
		sm.admissionWG.Wait()
		sm.mu.Lock()
		sessions := make([]*ProcessSession, 0, len(sm.sessions))
		for _, session := range sm.sessions {
			sessions = append(sessions, session)
		}
		closeErrors := append([]error(nil), sm.admissionErrors...)
		sm.mu.Unlock()
		for _, session := range sessions {
			if session == nil {
				continue
			}
			waitForCompletion := session.IsDone()
			if !waitForCompletion {
				killErr := session.Kill()
				switch {
				case killErr == nil, errors.Is(killErr, ErrSessionDone):
					waitForCompletion = true
				default:
					closeErrors = append(closeErrors, killErr)
				}
			}
			if waitForCompletion && session.hasCompletion() {
				if err := session.waitForCompletion(); err != nil {
					closeErrors = append(closeErrors, err)
				}
			}
		}
		sm.closeErr = errors.Join(closeErrors...)
		close(sm.closeDone)
	})
	<-sm.closeDone
	return sm.closeErr
}

func (sm *SessionManager) Get(sessionID string) (*ProcessSession, error) {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	session, ok := sm.sessions[sessionID]
	if !ok {
		return nil, ErrSessionNotFound
	}

	return session, nil
}

func (sm *SessionManager) Remove(sessionID string) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	delete(sm.sessions, sessionID)
}

func (sm *SessionManager) List() []toolshared.SessionInfo {
	sm.mu.RLock()
	defer sm.mu.RUnlock()

	result := make([]toolshared.SessionInfo, 0, len(sm.sessions))
	for _, session := range sm.sessions {
		result = append(result, session.ToSessionInfo())
	}

	return result
}

func generateSessionID() string {
	return uuid.New().String()[:8]
}
