package tools

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSessionManager_AddGet(t *testing.T) {
	sm := NewSessionManager()
	t.Cleanup(sm.Stop)
	session := &ProcessSession{
		ID:        "test-1",
		Command:   "echo hello",
		Status:    "running",
		StartTime: 1000,
	}

	sm.Add(session)

	got, err := sm.Get("test-1")
	require.NoError(t, err)
	require.Equal(t, "test-1", got.ID)
}

func TestSessionManager_Remove(t *testing.T) {
	sm := NewSessionManager()
	t.Cleanup(sm.Stop)
	session := &ProcessSession{
		ID:        "test-1",
		Command:   "echo hello",
		Status:    "running",
		StartTime: 1000,
	}
	sm.Add(session)
	sm.Remove("test-1")

	_, err := sm.Get("test-1")
	require.ErrorIs(t, err, ErrSessionNotFound)
}

func TestSessionManager_List(t *testing.T) {
	sm := NewSessionManager()
	t.Cleanup(sm.Stop)
	sm.Add(&ProcessSession{
		ID:        "test-1",
		Command:   "echo hello",
		Status:    "running",
		StartTime: 1000,
	})
	sm.Add(&ProcessSession{
		ID:        "test-2",
		Command:   "echo world",
		Status:    "running",
		StartTime: 1001,
	})
	sm.Add(&ProcessSession{
		ID:        "test-3",
		Command:   "echo done",
		Status:    "done",
		StartTime: 1002,
	})

	sessions := sm.List()
	require.Len(t, sessions, 3)

	ids := make(map[string]bool)
	for _, s := range sessions {
		ids[s.ID] = true
	}
	require.True(t, ids["test-1"])
	require.True(t, ids["test-2"])
	require.True(t, ids["test-3"])
}

func TestSessionManager_CloseRejectsLateAdmissionAndReturnsKillErrors(t *testing.T) {
	sm := NewSessionManager()
	sm.Add(&ProcessSession{ID: "unreachable", PID: -1, Status: "running"})
	require.ErrorIs(t, sm.Close(), ErrSessionNotFound)

	admitted := sm.Add(&ProcessSession{ID: "late", PID: -1, Status: "running"})
	require.False(t, admitted)
	_, err := sm.Get("late")
	require.ErrorIs(t, err, ErrSessionNotFound)
	require.ErrorIs(t, sm.Close(), ErrSessionNotFound)
}

func TestSessionManager_CloseWaitsForInFlightAdmissionCleanup(t *testing.T) {
	sm := NewSessionManager()
	admission, ok := sm.beginAdmission()
	require.True(t, ok)

	closed := make(chan error, 1)
	go func() {
		closed <- sm.Close()
	}()
	require.Eventually(t, func() bool {
		sm.mu.RLock()
		defer sm.mu.RUnlock()
		return sm.closing
	}, time.Second, time.Millisecond)
	select {
	case err := <-closed:
		t.Fatalf("Close() returned before admission cleanup: %v", err)
	default:
	}
	require.False(t, admission.admit(&ProcessSession{ID: "too-late"}))
	cleanupErr := errors.New("admission cleanup failed")
	admission.finish(cleanupErr)
	require.ErrorIs(t, <-closed, cleanupErr)
}

func TestSessionManager_ClosePropagatesAdmittedSessionWaitError(t *testing.T) {
	sm := NewSessionManager()
	session := &ProcessSession{
		ID: "wait-error", Status: "running", completion: make(chan struct{}),
	}
	require.True(t, sm.Add(session))
	waitErr := errors.New("wait failed")
	session.complete(-1, waitErr)
	require.ErrorIs(t, sm.Close(), waitErr)
}

func TestSessionManager_CloseReturnsPromptlyAfterTerminationFailure(t *testing.T) {
	sm := NewSessionManager()
	terminateErr := errors.New("termination failed")
	session := &ProcessSession{
		ID: "kill-error", PID: 42, Status: "running", completion: make(chan struct{}),
		terminate: func(int) error { return terminateErr },
	}
	require.True(t, sm.Add(session))
	closed := make(chan error, 1)
	go func() {
		closed <- sm.Close()
	}()
	select {
	case err := <-closed:
		require.ErrorIs(t, err, terminateErr)
	case <-time.After(time.Second):
		t.Fatal("Close() waited for completion after termination failed")
	}
}

func TestProcessSession_IsDone(t *testing.T) {
	session := &ProcessSession{Status: "running"}
	require.False(t, session.IsDone())

	session.Status = "done"
	require.True(t, session.IsDone())

	session.Status = "exited"
	require.True(t, session.IsDone())

	session.Status = "error"
	require.True(t, session.IsDone())
}

func TestProcessSession_ToSessionInfo(t *testing.T) {
	session := &ProcessSession{
		ID:        "test-1",
		PID:       12345,
		Command:   "echo hello",
		Status:    "running",
		StartTime: 1000,
	}

	info := session.ToSessionInfo()
	require.Equal(t, "test-1", info.ID)
	require.Equal(t, "echo hello", info.Command)
	require.Equal(t, "running", info.Status)
	require.Equal(t, 12345, info.PID)
	require.Equal(t, int64(1000), info.StartedAt)
}
