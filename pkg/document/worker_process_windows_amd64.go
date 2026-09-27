//go:build windows && amd64

package document

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	documentWorkerJobMemoryLimit  = 512 * 1024 * 1024
	documentWorkerJobProcessLimit = 2
	documentWorkerJobDrainTimeout = 2 * time.Second
)

var resumeDocumentWorkerProcess = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtResumeProcess")

type windowsDocumentWorkerProcessBoundary struct {
	command *exec.Cmd
	state   *windowsDocumentWorkerProcessState
}

type windowsDocumentWorkerProcessState struct {
	mu  sync.Mutex
	job windows.Handle
}

type documentWorkerJobAccounting struct {
	TotalUserTime             int64
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TotalTerminatedProcesses  uint32
}

func configureDocumentWorkerProcess(command *exec.Cmd) {
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	command.SysProcAttr.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_SUSPENDED
}

func configureDocumentWorkerInput(command *exec.Cmd, snapshot *os.File) (func(), error) {
	if command == nil || snapshot == nil {
		return nil, errors.New("document worker input is required")
	}
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	handle := windows.Handle(snapshot.Fd())
	if handle == 0 || handle == windows.InvalidHandle {
		return nil, errors.New("document worker input handle is invalid")
	}
	if err := windows.SetHandleInformation(
		handle,
		windows.HANDLE_FLAG_INHERIT,
		windows.HANDLE_FLAG_INHERIT,
	); err != nil {
		return nil, fmt.Errorf("mark document worker input inheritable: %w", err)
	}
	command.SysProcAttr.AdditionalInheritedHandles = append(
		command.SysProcAttr.AdditionalInheritedHandles,
		syscall.Handle(handle),
	)
	command.Env = append(command.Env, workerInputHandleEnvironment+"="+strconv.FormatUint(uint64(handle), 10))
	var once sync.Once
	return func() {
		once.Do(func() {
			_ = windows.SetHandleInformation(handle, windows.HANDLE_FLAG_INHERIT, 0)
		})
	}, nil
}

func prepareDocumentWorkerProcess(
	ctx context.Context,
	command *exec.Cmd,
	_ string,
	_ string,
) (documentWorkerProcessBoundary, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return nil, fmt.Errorf("create document worker job: %w", err)
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE |
		windows.JOB_OBJECT_LIMIT_ACTIVE_PROCESS |
		windows.JOB_OBJECT_LIMIT_JOB_MEMORY
	limits.BasicLimitInformation.ActiveProcessLimit = documentWorkerJobProcessLimit
	limits.JobMemoryLimit = documentWorkerJobMemoryLimit
	if _, err = windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)),
		uint32(unsafe.Sizeof(limits)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return nil, fmt.Errorf("configure document worker job: %w", err)
	}
	return &windowsDocumentWorkerProcessBoundary{
		command: command,
		state:   &windowsDocumentWorkerProcessState{job: job},
	}, nil
}

func (boundary *windowsDocumentWorkerProcessBoundary) started() error {
	if boundary == nil || boundary.command == nil || boundary.command.Process == nil || boundary.state == nil {
		return errors.New("document worker process boundary started without a process")
	}
	boundary.state.mu.Lock()
	defer boundary.state.mu.Unlock()
	if boundary.state.job == 0 {
		return errors.New("document worker process boundary is closed")
	}
	process, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_SUSPEND_RESUME,
		false,
		uint32(boundary.command.Process.Pid),
	)
	if err != nil {
		_ = boundary.command.Process.Kill()
		return fmt.Errorf("open suspended document worker: %w", err)
	}
	defer windows.CloseHandle(process)
	if err = windows.AssignProcessToJobObject(boundary.state.job, process); err != nil {
		_ = boundary.command.Process.Kill()
		return fmt.Errorf("assign document worker to job: %w", err)
	}
	status, _, callErr := resumeDocumentWorkerProcess.Call(uintptr(process))
	if status != 0 {
		return fmt.Errorf("resume document worker: NTSTATUS %#x: %v", status, callErr)
	}
	return nil
}

func (boundary *windowsDocumentWorkerProcessBoundary) terminate() error {
	if boundary == nil || boundary.state == nil {
		return nil
	}
	boundary.state.mu.Lock()
	defer boundary.state.mu.Unlock()
	return boundary.terminateLocked(documentWorkerJobDrainTimeout)
}

func (boundary *windowsDocumentWorkerProcessBoundary) close() error {
	return boundary.terminate()
}

func (boundary *windowsDocumentWorkerProcessBoundary) terminateLocked(timeout time.Duration) error {
	job := boundary.state.job
	if job == 0 {
		return nil
	}
	active, err := activeDocumentWorkerProcesses(job)
	if err != nil {
		return boundary.closeJobAfterError(job, err)
	}
	if active == 0 && boundary.command != nil && boundary.command.Process != nil {
		killErr := boundary.command.Process.Kill()
		if killErr != nil && !errors.Is(killErr, os.ErrProcessDone) {
			return boundary.closeJobAfterError(job, fmt.Errorf("kill unassigned document worker: %w", killErr))
		}
	}
	if active > 0 {
		if err = windows.TerminateJobObject(job, 1); err != nil {
			return boundary.closeJobAfterError(job, fmt.Errorf("terminate document worker job: %w", err))
		}
		deadline := time.Now().Add(timeout)
		for active > 0 {
			if time.Now().After(deadline) {
				return boundary.closeJobAfterError(
					job,
					fmt.Errorf("document worker job retained %d process(es)", active),
				)
			}
			time.Sleep(10 * time.Millisecond)
			active, err = activeDocumentWorkerProcesses(job)
			if err != nil {
				return boundary.closeJobAfterError(job, err)
			}
		}
	}
	if err = windows.CloseHandle(job); err != nil {
		return fmt.Errorf("close document worker job: %w", err)
	}
	boundary.state.job = 0
	return nil
}

func (boundary *windowsDocumentWorkerProcessBoundary) closeJobAfterError(job windows.Handle, cause error) error {
	closeErr := windows.CloseHandle(job)
	boundary.state.job = 0
	if closeErr != nil {
		return errors.Join(cause, fmt.Errorf("close document worker job: %w", closeErr))
	}
	return cause
}

func activeDocumentWorkerProcesses(job windows.Handle) (uint32, error) {
	var info documentWorkerJobAccounting
	err := windows.QueryInformationJobObject(
		job,
		windows.JobObjectBasicAccountingInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
		nil,
	)
	if err != nil {
		return 0, fmt.Errorf("query document worker job: %w", err)
	}
	return info.ActiveProcesses, nil
}
