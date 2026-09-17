//go:build windows

package workerprocess

import (
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

var resumeWorkerProcess = windows.NewLazySystemDLL("ntdll.dll").NewProc("NtResumeProcess")

type processDomain struct {
	command *exec.Cmd
	state   *windowsProcessDomainState
}

type windowsProcessDomainState struct {
	mu  sync.Mutex
	job windows.Handle
}

type jobObjectBasicAccountingInformation struct {
	TotalUserTime             int64
	TotalKernelTime           int64
	ThisPeriodTotalUserTime   int64
	ThisPeriodTotalKernelTime int64
	TotalPageFaultCount       uint32
	TotalProcesses            uint32
	ActiveProcesses           uint32
	TotalTerminatedProcesses  uint32
}

func prepareProcessDomain(command *exec.Cmd) (processDomain, error) {
	if command == nil {
		return processDomain{}, errors.New("coding worker command is required")
	}
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	// Suspended startup closes the gap in which the worker could create a
	// descendant before the parent assigns it to the kill-on-close job.
	command.SysProcAttr.CreationFlags |= windows.CREATE_NEW_PROCESS_GROUP | windows.CREATE_SUSPENDED
	job, err := windows.CreateJobObject(nil, nil)
	if err != nil {
		return processDomain{}, fmt.Errorf("create coding worker process domain: %w", err)
	}
	limits := windows.JOBOBJECT_EXTENDED_LIMIT_INFORMATION{}
	limits.BasicLimitInformation.LimitFlags = windows.JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE
	if _, err = windows.SetInformationJobObject(
		job,
		windows.JobObjectExtendedLimitInformation,
		uintptr(unsafe.Pointer(&limits)),
		uint32(unsafe.Sizeof(limits)),
	); err != nil {
		_ = windows.CloseHandle(job)
		return processDomain{}, fmt.Errorf("configure coding worker process domain: %w", err)
	}
	return processDomain{
		command: command,
		state:   &windowsProcessDomainState{job: job},
	}, nil
}

func (domain processDomain) started() error {
	if domain.command == nil || domain.command.Process == nil || domain.state == nil {
		return errors.New("coding worker process domain started without a process")
	}
	domain.state.mu.Lock()
	defer domain.state.mu.Unlock()
	if domain.state.job == 0 {
		return errors.New("coding worker process domain is closed")
	}
	process, err := windows.OpenProcess(
		windows.PROCESS_SET_QUOTA|windows.PROCESS_TERMINATE|windows.PROCESS_SUSPEND_RESUME,
		false,
		uint32(domain.command.Process.Pid),
	)
	if err != nil {
		return fmt.Errorf("open suspended coding worker: %w", err)
	}
	defer windows.CloseHandle(process)
	if err = windows.AssignProcessToJobObject(domain.state.job, process); err != nil {
		return fmt.Errorf("assign coding worker process domain: %w", err)
	}
	status, _, callErr := resumeWorkerProcess.Call(uintptr(process))
	if status != 0 {
		return fmt.Errorf("resume coding worker process: NTSTATUS %#x: %v", status, callErr)
	}
	return nil
}

func (domain processDomain) stop(timeout time.Duration) error {
	if domain.state == nil {
		return nil
	}
	domain.state.mu.Lock()
	defer domain.state.mu.Unlock()
	return domain.stopLocked(timeout)
}

func (domain processDomain) close() error {
	if domain.state == nil {
		return nil
	}
	domain.state.mu.Lock()
	defer domain.state.mu.Unlock()
	return domain.stopLocked(DefaultStopTimeout)
}

func (domain processDomain) stopLocked(timeout time.Duration) error {
	job := domain.state.job
	if job == 0 {
		return nil
	}
	active, err := activeWorkerProcesses(job)
	if err != nil {
		return err
	}
	// A clean worker exit should have closed its own children. If it did not,
	// the job remains the last-resort generation boundary and must be drained
	// before Wait reports completion.
	if active > 0 {
		if err = windows.TerminateJobObject(job, 1); err != nil {
			return fmt.Errorf("terminate coding worker process domain: %w", err)
		}
		if timeout <= 0 {
			timeout = DefaultStopTimeout
		}
		deadline := time.Now().Add(timeout)
		for active > 0 {
			if time.Now().After(deadline) {
				return fmt.Errorf("coding worker process domain retained %d process(es)", active)
			}
			time.Sleep(10 * time.Millisecond)
			active, err = activeWorkerProcesses(job)
			if err != nil {
				return err
			}
		}
	}
	if err = windows.CloseHandle(job); err != nil {
		return fmt.Errorf("close coding worker process domain: %w", err)
	}
	domain.state.job = 0
	return nil
}

func activeWorkerProcesses(job windows.Handle) (uint32, error) {
	var info jobObjectBasicAccountingInformation
	err := windows.QueryInformationJobObject(
		job,
		windows.JobObjectBasicAccountingInformation,
		uintptr(unsafe.Pointer(&info)),
		uint32(unsafe.Sizeof(info)),
		nil,
	)
	if err != nil {
		return 0, fmt.Errorf("query coding worker process domain: %w", err)
	}
	return info.ActiveProcesses, nil
}
