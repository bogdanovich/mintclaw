//go:build linux || darwin

package provisioning

import (
	"context"
	"encoding/base64"
	"os"
	"time"

	"golang.org/x/sys/unix"
)

// Provision stores one encrypted credential for a future qualified service. It
// requires a controlling operator terminal, accepts no environment or argument
// token, and refuses replacement. Service readiness and activation are separate.
func Provision(ctx context.Context) error {
	if ctx == nil || ctx.Err() != nil {
		return ErrInput
	}
	if err := protectProcess(); err != nil {
		return err
	}
	fd, err := unix.Open("/", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return ErrStorage
	}
	root := os.NewFile(uintptr(fd), "/")
	defer func() { _ = root.Close() }()
	if err := checkEncryptionTool(root, 0); err != nil {
		return err
	}
	return provisionAt(ctx, root, 0, readOperatorToken, encryptToken)
}

func provisionAt(
	ctx context.Context,
	root *os.File,
	owner uint32,
	read func(context.Context) ([]byte, error),
	encrypt func(context.Context, []byte) ([]byte, error),
) error {
	dir, err := openCredentialDirectory(root, owner)
	if err != nil {
		return err
	}
	defer func() { _ = dir.Close() }()
	if lockErr := unix.Flock(int(dir.Fd()), unix.LOCK_EX|unix.LOCK_NB); lockErr != nil {
		return ErrBusy
	}
	if existsErr := requireAbsent(dir); existsErr != nil {
		return existsErr
	}
	if ctx == nil || ctx.Err() != nil {
		return ErrInput
	}
	inputCtx, stopInput := context.WithTimeout(ctx, 5*time.Minute)
	defer stopInput()
	token, err := read(inputCtx)
	defer clear(token)
	if err != nil || inputCtx.Err() != nil {
		return ErrInput
	}
	if !validToken(token) {
		return ErrToken
	}
	commandCtx, stopCommand := context.WithTimeout(ctx, 30*time.Second)
	defer stopCommand()
	ciphertext, err := encrypt(commandCtx, token)
	defer clear(ciphertext)
	if err != nil || commandCtx.Err() != nil || !validCiphertext(ciphertext) {
		return ErrEncryption
	}
	return persistCiphertext(ctx, dir, ciphertext)
}

func validToken(token []byte) bool {
	if len(token) == 0 || len(token) > maxTokenBytes {
		return false
	}
	for _, value := range token {
		if value < 0x21 || value > 0x7e {
			return false
		}
	}
	return true
}

// This is a bounded output-format check, not a cryptographic verifier. The
// trusted OS helper owns encryption and the future system service authenticates
// decryption. Refuse raw diagnostics or unexpectedly large helper output.
func validCiphertext(data []byte) bool {
	if len(data) == 0 || len(data) > maxCiphertextBytes {
		return false
	}
	decoded := make([]byte, base64.StdEncoding.DecodedLen(len(data)))
	defer clear(decoded)
	size, err := base64.StdEncoding.Strict().Decode(decoded, data)
	return err == nil && size >= 48
}
