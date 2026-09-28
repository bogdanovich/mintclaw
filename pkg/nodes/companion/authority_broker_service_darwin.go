//go:build darwin

package companion

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	darwinAuthorityCompanionStopTimeout = 10 * time.Second
	authorityBrokerHandshakeTimeout     = 5 * time.Second
	authorityBrokerResponseWriteTimeout = time.Second
)

type darwinAuthorityBrokerServer struct {
	config     AuthorityBrokerConfig
	runner     darwinAuthorityExecutionRunner
	semaphores map[string]chan struct{}
	calls      chan struct{}
}

type darwinAuthorityExecutionRunner interface {
	Execute(
		context.Context,
		preparedAuthorityBrokerExecution,
		ShellBrokerRequest,
	) (ShellBrokerResult, error)
}

func newDarwinAuthorityBrokerServer(
	config AuthorityBrokerConfig,
) (*darwinAuthorityBrokerServer, error) {
	if config.Companion == nil || len(config.normalizedProfile) != MaxShellBrokerProfiles {
		return nil, errors.New("macOS authority broker configuration is incomplete")
	}
	semaphores := make(map[string]chan struct{}, len(config.normalizedProfile))
	for alias, profile := range config.normalizedProfile {
		semaphores[alias] = make(chan struct{}, profile.ConcurrentCommands)
	}
	return &darwinAuthorityBrokerServer{
		config: config, runner: &darwinAuthorityBrokerProcessRunner{}, semaphores: semaphores,
		calls: make(chan struct{}, maxAuthorityBrokerConcurrentCalls),
	}, nil
}

func RunAuthorityBroker(
	ctx context.Context,
	config AuthorityBrokerConfig,
	executable string,
) error {
	if os.Geteuid() != 0 {
		return errors.New("authority broker must run as root")
	}
	if err := verifyDarwinAuthorityManagedFile(executable, true); err != nil {
		return fmt.Errorf("verify authority broker executable: %w", err)
	}
	if config.Companion == nil || len(config.normalizedProfile) != MaxShellBrokerProfiles {
		return errors.New("authority broker config is not normalized")
	}
	if err := verifyDarwinAuthorityManagedFile(config.Companion.ExecutablePath, true); err != nil {
		return fmt.Errorf("verify companion executable: %w", err)
	}
	if err := verifyDarwinAuthorityManagedFile(config.Companion.ConfigPath, false); err != nil {
		return fmt.Errorf("verify companion config: %w", err)
	}
	for alias, profile := range config.normalizedProfile {
		if err := verifyDarwinAuthorityManagedFile(profile.ShellPath, true); err != nil {
			return fmt.Errorf("verify authority profile %q shell: %w", alias, err)
		}
	}
	server, err := newDarwinAuthorityBrokerServer(config)
	if err != nil {
		return err
	}
	return runDarwinSupervisedAuthorityBroker(ctx, config, server)
}

func runDarwinSupervisedAuthorityBroker(
	ctx context.Context,
	config AuthorityBrokerConfig,
	server *darwinAuthorityBrokerServer,
) error {
	sockets, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return fmt.Errorf("create macOS authority capability: %w", err)
	}
	unix.CloseOnExec(sockets[0])
	unix.CloseOnExec(sockets[1])
	parentFile := os.NewFile(uintptr(sockets[0]), "authority-factory-parent")
	childFile := os.NewFile(uintptr(sockets[1]), "authority-factory-child")
	if parentFile == nil || childFile == nil {
		if parentFile != nil {
			_ = parentFile.Close()
		}
		if childFile != nil {
			_ = childFile.Close()
		}
		return errors.New("open macOS authority capability")
	}
	connection, err := net.FileConn(parentFile)
	_ = parentFile.Close()
	if err != nil {
		_ = childFile.Close()
		return errors.New("adopt macOS authority capability")
	}
	factory, ok := connection.(*net.UnixConn)
	if !ok {
		_ = connection.Close()
		_ = childFile.Close()
		return errors.New("macOS authority capability is not Unix")
	}
	defer func() { _ = factory.Close() }()
	companion := config.Companion
	home, err := darwinAuthorityCompanionHome(companion.UID)
	if err != nil {
		_ = childFile.Close()
		return err
	}
	command := exec.Command(
		companion.ExecutablePath,
		"run",
		"--config",
		companion.ConfigPath,
	)
	command.Env = []string{
		"HOME=" + home,
		"PATH=/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin",
		"TMPDIR=/tmp",
		AuthorityBrokerEnvironmentFD + "=" + strconv.Itoa(authorityBrokerDarwinFD),
	}
	command.ExtraFiles = []*os.File{childFile}
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	prepareJobProcess(command)
	if os.Geteuid() == 0 {
		command.SysProcAttr.Credential = &syscall.Credential{
			Uid:    companion.UID,
			Gid:    companion.GID,
			Groups: append([]uint32(nil), companion.SupplementaryGroups...),
		}
	} else if companion.UID != uint32(os.Geteuid()) || companion.GID != uint32(os.Getegid()) ||
		len(companion.SupplementaryGroups) != 0 {
		_ = childFile.Close()
		return errors.New("unprivileged supervisor fixture cannot change identity")
	}
	if err = command.Start(); err != nil {
		_ = childFile.Close()
		return errors.New("start supervised macOS companion")
	}
	if err = childFile.Close(); err != nil {
		_ = command.Process.Kill()
		_ = command.Wait()
		return fmt.Errorf("close macOS companion capability: %w", err)
	}
	serverContext, cancelServer := context.WithCancel(ctx)
	defer cancelServer()
	serverDone := make(chan error, 1)
	go func() { serverDone <- server.ServeFactory(serverContext, factory) }()
	childDone := make(chan error, 1)
	go func() { childDone <- command.Wait() }()
	select {
	case childErr := <-childDone:
		cancelServer()
		_ = factory.Close()
		<-serverDone
		if ctx.Err() != nil {
			return nil
		}
		if childErr == nil {
			return errors.New("supervised macOS companion exited unexpectedly")
		}
		return fmt.Errorf("supervised macOS companion exited: %w", childErr)
	case serverErr := <-serverDone:
		stopDarwinSupervisedCompanion(command, childDone)
		if ctx.Err() != nil {
			return nil
		}
		if serverErr == nil {
			return errors.New("macOS authority capability stopped unexpectedly")
		}
		return fmt.Errorf("macOS authority capability failed: %w", serverErr)
	case <-ctx.Done():
		cancelServer()
		_ = factory.Close()
		stopDarwinSupervisedCompanion(command, childDone)
		<-serverDone
		return nil
	}
}

func stopDarwinSupervisedCompanion(command *exec.Cmd, done <-chan error) {
	if command == nil || command.Process == nil {
		return
	}
	_ = syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
	timer := time.NewTimer(darwinAuthorityCompanionStopTimeout)
	defer timer.Stop()
	select {
	case <-done:
	case <-timer.C:
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		<-done
	}
}

func darwinAuthorityCompanionHome(uid uint32) (string, error) {
	account, err := user.LookupId(strconv.FormatUint(uint64(uid), 10))
	if err != nil || account == nil || !filepath.IsAbs(account.HomeDir) ||
		filepath.Clean(account.HomeDir) != account.HomeDir {
		return "", errors.New("resolve supervised macOS companion home")
	}
	return account.HomeDir, nil
}

func verifyDarwinAuthorityManagedFile(path string, executable bool) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("managed path is not clean and absolute")
	}
	if err := verifyAuthorityBrokerDirectoryChain(filepath.Dir(path)); err != nil {
		return err
	}
	info, err := os.Lstat(path)
	stat, ok := pathInfoSyscallStat(info)
	if err != nil || !ok || stat.Uid != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o022 != 0 {
		return errors.New("managed file must be root-owned, regular, and non-writable")
	}
	if executable && info.Mode().Perm()&0o111 == 0 {
		return errors.New("managed executable is not executable")
	}
	return nil
}

func (server *darwinAuthorityBrokerServer) ServeFactory(
	ctx context.Context,
	factory *net.UnixConn,
) error {
	if server == nil || factory == nil {
		return errors.New("macOS authority broker factory is unavailable")
	}
	go func() {
		<-ctx.Done()
		_ = factory.Close()
	}()
	var workers sync.WaitGroup
	defer workers.Wait()
	for {
		request := make([]byte, 1)
		control := make([]byte, unix.CmsgSpace(4*MaxAuthorityBrokerGroups))
		n, controlN, flags, _, err := factory.ReadMsgUnix(request, control)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		if controlN > 0 {
			closeDarwinAuthorityReceivedRights(control[:controlN])
		}
		if n != 1 || request[0] != authorityBrokerFactoryRequest || controlN != 0 ||
			flags&unix.MSG_CTRUNC != 0 {
			return errors.New("invalid macOS authority broker factory request")
		}
		select {
		case server.calls <- struct{}{}:
		default:
			if err = writeDarwinAuthorityFactoryResponse(factory, authorityBrokerFactoryBusy, nil); err != nil {
				return err
			}
			continue
		}
		connection, descriptor, err := newDarwinAuthorityCallConnection()
		if err != nil {
			<-server.calls
			return err
		}
		rights := unix.UnixRights(descriptor)
		sendErr := writeDarwinAuthorityFactoryResponse(
			factory,
			authorityBrokerFactoryRequest,
			rights,
		)
		_ = unix.Close(descriptor)
		if sendErr != nil {
			_ = connection.Close()
			<-server.calls
			return errors.New("send macOS authority broker connection")
		}
		workers.Add(1)
		go func() {
			defer workers.Done()
			defer func() { <-server.calls }()
			defer func() { _ = connection.Close() }()
			server.handleConnection(ctx, connection)
		}()
	}
}

func writeDarwinAuthorityFactoryResponse(
	factory *net.UnixConn,
	status byte,
	rights []byte,
) error {
	if err := factory.SetWriteDeadline(time.Now().Add(authorityBrokerHandshakeTimeout)); err != nil {
		return err
	}
	defer func() { _ = factory.SetWriteDeadline(time.Time{}) }()
	written, controlWritten, err := factory.WriteMsgUnix([]byte{status}, rights, nil)
	if err != nil {
		return err
	}
	if written != 1 || controlWritten != len(rights) {
		return errors.New("short macOS authority broker factory response")
	}
	return nil
}

func closeDarwinAuthorityReceivedRights(control []byte) {
	messages, err := unix.ParseSocketControlMessage(control)
	if err != nil {
		return
	}
	for index := range messages {
		descriptors, parseErr := unix.ParseUnixRights(&messages[index])
		if parseErr != nil {
			continue
		}
		for _, descriptor := range descriptors {
			_ = unix.Close(descriptor)
		}
	}
}

func newDarwinAuthorityCallConnection() (*net.UnixConn, int, error) {
	descriptors, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		return nil, -1, err
	}
	unix.CloseOnExec(descriptors[0])
	unix.CloseOnExec(descriptors[1])
	file := os.NewFile(uintptr(descriptors[0]), "authority-call-server")
	if file == nil {
		_ = unix.Close(descriptors[0])
		_ = unix.Close(descriptors[1])
		return nil, -1, errors.New("open macOS authority broker call")
	}
	connection, err := net.FileConn(file)
	_ = file.Close()
	if err != nil {
		_ = unix.Close(descriptors[1])
		return nil, -1, err
	}
	unixConnection, ok := connection.(*net.UnixConn)
	if !ok {
		_ = connection.Close()
		_ = unix.Close(descriptors[1])
		return nil, -1, errors.New("macOS authority broker call is not Unix")
	}
	return unixConnection, descriptors[1], nil
}

func (server *darwinAuthorityBrokerServer) handleConnection(
	serverContext context.Context,
	connection *net.UnixConn,
) {
	_ = connection.SetReadDeadline(time.Now().Add(authorityBrokerHandshakeTimeout))
	var request authorityBrokerRequestFrame
	if err := readAuthorityBrokerFrame(connection, &request); err != nil {
		return
	}
	_ = connection.SetReadDeadline(time.Time{})
	if err := validateAuthorityBrokerRequestFrame(request); err != nil {
		_ = server.writeResponse(connection, authorityBrokerResponseFrame{Code: "INVALID_REQUEST"})
		return
	}
	if request.Action == authorityBrokerActionSnapshot {
		snapshot, err := server.config.Snapshot()
		if err != nil {
			_ = server.writeResponse(connection, authorityBrokerResponseFrame{Code: "UNAVAILABLE"})
			return
		}
		_ = server.writeResponse(connection, authorityBrokerResponseFrame{OK: true, Snapshot: &snapshot})
		return
	}
	if request.Action == authorityBrokerActionTerminal {
		_ = server.writeResponse(connection, authorityBrokerResponseFrame{Code: "UNAVAILABLE"})
		return
	}
	prepared, err := server.config.prepareExecution(*request.Execute)
	if err != nil {
		_ = server.writeResponse(connection, authorityBrokerResponseFrame{Code: "DENIED"})
		return
	}
	executeContext, cancel := context.WithCancel(serverContext)
	defer cancel()
	peerClosed := make(chan struct{})
	go func() {
		var discarded [1]byte
		_, _ = connection.Read(discarded[:])
		cancel()
		close(peerClosed)
	}()
	semaphore := server.semaphores[request.Execute.Profile]
	select {
	case semaphore <- struct{}{}:
		defer func() { <-semaphore }()
	case <-executeContext.Done():
		_ = server.writeResponse(connection, authorityBrokerResponseFrame{Code: "UNKNOWN"})
		return
	}
	result, executeErr := server.runner.Execute(executeContext, prepared, *request.Execute)
	response := authorityBrokerResponseFrame{Result: &result}
	if executeErr != nil {
		response = authorityBrokerResponseFrame{Code: "UNKNOWN"}
	} else {
		response.OK = true
	}
	if err := server.writeResponse(connection, response); err != nil {
		return
	}
	_ = connection.CloseRead()
	select {
	case <-peerClosed:
	case <-time.After(time.Second):
	}
}

func (*darwinAuthorityBrokerServer) writeResponse(
	connection *net.UnixConn,
	response authorityBrokerResponseFrame,
) error {
	response.Version = AuthorityBrokerProtocolVersion
	if err := connection.SetWriteDeadline(time.Now().Add(authorityBrokerResponseWriteTimeout)); err != nil {
		return err
	}
	return writeAuthorityBrokerFrame(connection, response)
}
