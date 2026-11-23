//go:build linux && amd64

package document

import (
	"errors"
	"os"
	"os/exec"

	"github.com/bogdanovich/mintclaw/pkg/isolation"
)

const popplerStderrLimit = 8 * 1024

func popplerBackendAvailable() bool {
	return nativeBackendAvailable(PopplerBackendName)
}

func newVerifiedPopplerCommand(executable, expectedSHA256 string, arguments ...string) (*exec.Cmd, *os.File, error) {
	return newVerifiedDocumentCommand("mintclaw-poppler", executable, expectedSHA256, arguments...)
}

func newVerifiedDocumentCommand(
	snapshotName string,
	executable string,
	expectedSHA256 string,
	arguments ...string,
) (*exec.Cmd, *os.File, error) {
	source, err := openSourceNoFollow(executable)
	if err != nil {
		return nil, nil, errors.New("document native backend is unavailable")
	}
	defer func() { _ = source.Close() }()
	command, snapshot, err := isolation.NewSealedExecutableCommand(
		snapshotName, source, expectedSHA256, maximumExecutableBytes, arguments...,
	)
	if errors.Is(err, isolation.ErrSealedExecutableIdentity) {
		return nil, nil, errors.New("document native backend identity is not admitted")
	}
	if err != nil {
		return nil, nil, errors.New("document native backend cannot be isolated")
	}
	return command, snapshot, nil
}

func documentBackendEnvironment() []string {
	return []string{"HOME=.", "TMPDIR=.", "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "PATH="}
}
