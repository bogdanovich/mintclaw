//go:build linux

package startup

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/user"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/bogdanovich/mintclaw/cmd/mintclaw-credential/internal/approval"
)

const (
	approvalConfigPath       = "/etc/mintclaw-credential/approval.json"
	approvalConsoleDirectory = "/run/mintclaw-credential-owner"
)

// ServeApproval activates only the protected phone/enrollment service. It never
// invokes the token loader, initializes the SDK or creates an agent listener.
// Root-controlled deployment must establish this dedicated service role outside
// all agent administration reach before initial owner enrollment.
func ServeApproval(ctx context.Context) error {
	if ctx == nil || ctx.Err() != nil {
		return errUnavailable
	}
	for _, key := range []string{"OP_SERVICE_ACCOUNT_TOKEN", "CREDENTIALS_DIRECTORY", "GODEBUG", "LD_PRELOAD", "LD_LIBRARY_PATH"} {
		if _, exists := os.LookupEnv(key); exists {
			return errUnavailable
		}
	}
	identity, err := protectServiceProcess()
	if err != nil {
		return errUnavailable
	}
	account, err := user.Lookup("mintclaw-credential")
	if err != nil || account.Uid != strconv.FormatUint(uint64(identity.uid), 10) ||
		account.Gid != strconv.FormatUint(uint64(identity.gid), 10) {
		return errUnavailable
	}
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return errUnavailable
	}
	fields := make(map[string]string)
	for _, line := range strings.Split(string(status), "\n") {
		name, value, found := strings.Cut(line, ":")
		if found {
			fields[name] = strings.TrimSpace(value)
		}
	}
	if fields["CapBnd"] != "0000000000000000" || fields["CapAmb"] != "0000000000000000" {
		return errUnavailable
	}
	return serveApproval(ctx, approvalConfigPath, approvalConsoleDirectory, identity)
}

func serveApproval(
	ctx context.Context,
	configPath, consoleDirectory string,
	identity serviceIdentity,
) (resultErr error) {
	config, err := loadApprovalConfig(ctx, configPath, identity)
	if err != nil {
		return errUnavailable
	}
	certificate, err := approvalCertificate(ctx, config, identity)
	if err != nil {
		return errUnavailable
	}
	controller, setup, err := approval.New(approval.Config{
		OwnerID: config.OwnerID, Origin: config.Origin,
		StateDirectory: config.StateDirectory, Accounts: config.Accounts,
	})
	if err != nil {
		return errUnavailable
	}
	defer func() {
		if controller.Close() != nil {
			resultErr = errUnavailable
		}
	}()
	phone, err := approval.NewPhoneHandler(controller)
	if err != nil {
		return errUnavailable
	}
	var enrollment *approval.Enrollment
	var console *approvalConsoleSocket
	if setup != nil {
		enrollment, err = approval.NewEnrollment(setup)
		if err != nil {
			return errUnavailable
		}
		console, err = approvalConsole(consoleDirectory, identity)
		if err != nil {
			return errUnavailable
		}
		defer func() {
			if console.Close() != nil {
				resultErr = errUnavailable
			}
		}()
	}
	listener, err := net.Listen("tcp", config.ListenAddress)
	if err != nil {
		return errUnavailable
	}
	defer func() { _ = listener.Close() }()
	lifetime, cancel := context.WithCancel(ctx)
	defer cancel()
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if enrollment != nil && request.URL != nil &&
			(request.URL.Path == "/api/enroll/begin" || request.URL.Path == "/api/enroll/finish") {
			enrollment.ServeHTTP(writer, request)
			return
		}
		phone.ServeHTTP(writer, request)
	})
	parsed, _ := url.Parse(config.Origin)
	server := &http.Server{
		Handler: handler, ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout: 10 * time.Second, WriteTimeout: 12 * time.Second, IdleTimeout: 15 * time.Second,
		MaxHeaderBytes: 8192, ErrorLog: log.New(io.Discard, "", 0),
		BaseContext:  func(net.Listener) context.Context { return lifetime },
		TLSNextProto: make(map[string]func(*http.Server, *tls.Conn, http.Handler)),
		TLSConfig: &tls.Config{
			MinVersion:   tls.VersionTLS13,
			Certificates: []tls.Certificate{certificate},
			NextProtos: []string{
				"http/1.1",
			},
			GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
				if hello.ServerName != parsed.Hostname() {
					return nil, errUnavailable
				}
				return nil, nil
			},
		},
	}
	var workers sync.WaitGroup
	var retirementErr error // Written by the shutdown worker; read after Wait.
	if console != nil {
		workers.Add(1)
		go func() {
			defer workers.Done()
			if enrollment.ServeConsole(lifetime, console.listener) != nil {
				cancel()
			}
		}()
	}
	workers.Add(1)
	go func() {
		defer workers.Done()
		<-lifetime.Done()
		// Withdraw all authority before waiting for HTTP/console work to drain.
		retirementErr = controller.Close()
		if console != nil {
			_ = console.Close()
		}
		shutdown, done := context.WithTimeout(context.Background(), 12*time.Second)
		defer done()
		if server.Shutdown(shutdown) != nil {
			_ = server.Close()
		}
	}()
	serveErr := server.ServeTLS(&approvalListener{Listener: listener, slots: make(chan struct{}, 128)}, "", "")
	cancel()
	workers.Wait()
	if retirementErr != nil {
		return errUnavailable
	}
	if ctx.Err() != nil && errors.Is(serveErr, http.ErrServerClosed) {
		return nil
	}
	return errUnavailable
}

func approvalCertificate(
	ctx context.Context,
	config approvalConfig,
	identity serviceIdentity,
) (tls.Certificate, error) {
	encoded, err := readApprovalFile(ctx, config.CertificateFile, identity)
	if err != nil {
		return tls.Certificate{}, errUnavailable
	}
	defer clear(encoded)
	key, err := readApprovalFile(ctx, config.PrivateKeyFile, identity)
	if err != nil {
		return tls.Certificate{}, errUnavailable
	}
	defer clear(key)
	certificate, err := tls.X509KeyPair(encoded, key)
	if err != nil || len(certificate.Certificate) == 0 {
		return tls.Certificate{}, errUnavailable
	}
	leaf, err := x509.ParseCertificate(certificate.Certificate[0])
	parsed, _ := url.Parse(config.Origin)
	if err != nil || leaf.VerifyHostname(parsed.Hostname()) != nil || time.Now().Before(leaf.NotBefore) ||
		!time.Now().Before(leaf.NotAfter) || ctx.Err() != nil {
		return tls.Certificate{}, errUnavailable
	}
	return certificate, nil
}

func approvalConsole(path string, identity serviceIdentity) (*approvalConsoleSocket, error) {
	if !validDirectoryPath(path) {
		return nil, errUnavailable
	}
	// Pin all Root ancestors, then the service-only final directory. No stale
	// socket is removed or replaced; uncertain setup requires protected repair.
	descriptor, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, errUnavailable
	}
	parts := strings.Split(strings.TrimPrefix(path, "/"), "/")
	for _, part := range parts {
		var stat unix.Stat_t
		acl, aclErr := descriptorACL(descriptor)
		if unix.Fstat(descriptor, &stat) != nil || stat.Uid != 0 || stat.Mode&0o022 != 0 || aclErr != nil ||
			len(acl) != 0 {
			_ = unix.Close(descriptor)
			return nil, errUnavailable
		}
		next, openErr := unix.Openat(descriptor, part, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		_ = unix.Close(descriptor)
		if openErr != nil {
			return nil, errUnavailable
		}
		descriptor = next
	}
	var stat unix.Stat_t
	acl, aclErr := descriptorACL(descriptor)
	if unix.Fstat(descriptor, &stat) != nil || stat.Uid != identity.uid || stat.Gid != identity.gid ||
		stat.Mode&0o7777 != 0o700 || aclErr != nil || len(acl) != 0 {
		_ = unix.Close(descriptor)
		return nil, errUnavailable
	}
	pinned := "/proc/self/fd/" + strconv.Itoa(descriptor) + "/setup.sock"
	listener, err := net.ListenUnix("unixpacket", &net.UnixAddr{Name: pinned, Net: "unixpacket"})
	if err != nil {
		_ = unix.Close(descriptor)
		return nil, errUnavailable
	}
	listener.SetUnlinkOnClose(false)
	var socket unix.Stat_t
	if unix.Fchmodat(descriptor, "setup.sock", 0o600, 0) != nil ||
		unix.Fstatat(descriptor, "setup.sock", &socket, unix.AT_SYMLINK_NOFOLLOW) != nil ||
		socket.Mode&unix.S_IFMT != unix.S_IFSOCK || socket.Mode&0o7777 != 0o600 ||
		socket.Uid != identity.uid || socket.Gid != identity.gid {
		_ = listener.Close()
		_ = unix.Close(descriptor)
		return nil, errUnavailable
	}
	// The protected path stays pinned for the socket's complete lifetime.
	return &approvalConsoleSocket{listener: listener, directory: descriptor, inode: socket}, nil
}

type approvalConsoleSocket struct {
	listener  *net.UnixListener
	directory int
	inode     unix.Stat_t
	once      sync.Once
	closeErr  error
}

func (socket *approvalConsoleSocket) Close() error {
	socket.once.Do(func() {
		_ = socket.listener.Close()
		var current unix.Stat_t
		if unix.Fstatat(socket.directory, "setup.sock", &current, unix.AT_SYMLINK_NOFOLLOW) != nil ||
			current.Dev != socket.inode.Dev || current.Ino != socket.inode.Ino || current.Mode != socket.inode.Mode ||
			current.Uid != socket.inode.Uid || current.Gid != socket.inode.Gid ||
			unix.Unlinkat(socket.directory, "setup.sock", 0) != nil {
			socket.closeErr = errUnavailable
		}
		if unix.Close(socket.directory) != nil {
			socket.closeErr = errUnavailable
		}
	})
	return socket.closeErr
}

type approvalListener struct {
	net.Listener
	slots chan struct{}
}
type approvalConnection struct {
	net.Conn
	release func()
	once    sync.Once
}

func (listener *approvalListener) Accept() (net.Conn, error) {
	for {
		connection, err := listener.Listener.Accept()
		if err != nil {
			return nil, err
		}
		select {
		case listener.slots <- struct{}{}:
			return &approvalConnection{Conn: connection, release: func() { <-listener.slots }}, nil
		default:
			_ = connection.Close()
		}
	}
}

func (connection *approvalConnection) Close() error {
	connection.once.Do(func() { _ = connection.Conn.Close(); connection.release() })
	return nil
}
