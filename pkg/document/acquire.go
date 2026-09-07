package document

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/google/uuid"
)

const (
	operationAcquire = "acquire"
	pdfHeaderBytes   = 1024
)

type AcquireOptions struct {
	ScratchRoot string
	MaxBytes    int64
}

type Snapshot struct {
	path      string
	dir       string
	removeAll func(string) error
}

func (s *Snapshot) Path() string {
	if s == nil {
		return ""
	}
	return s.path
}

func (s *Snapshot) Close() error {
	if s == nil || s.dir == "" {
		return nil
	}
	removeAll := os.RemoveAll
	if s.removeAll != nil {
		removeAll = s.removeAll
	}
	err := removeAll(s.dir)
	if err == nil {
		s.path = ""
		s.dir = ""
	}
	return err
}

func Acquire(ctx context.Context, inputPath string, options AcquireOptions) (*Snapshot, Report) {
	return acquireForPlatform(ctx, inputPath, options, runtime.GOOS, runtime.GOARCH)
}

func acquireForPlatform(
	ctx context.Context,
	inputPath string,
	options AcquireOptions,
	goos string,
	goarch string,
) (*Snapshot, Report) {
	operationID := "document_operation_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	maxBytes := options.MaxBytes
	if maxBytes <= 0 || maxBytes > DefaultMaxInputBytes {
		maxBytes = DefaultMaxInputBytes
	}
	report := newReport(operationID, maxBytes)

	capability := capabilitiesFor(goos, goarch).Operations[operationAcquire]
	if capability.State != CapabilitySupported {
		return nil, failReport(
			report,
			StateUnavailable,
			FailureUnsupportedPlatform,
			capability.Reason,
		)
	}
	return acquireSnapshot(ctx, inputPath, options.ScratchRoot, report, nil)
}

func newReport(operationID string, maxBytes int64) Report {
	return Report{
		SchemaVersion: ReportSchemaVersion,
		OperationID:   operationID,
		Operation:     operationAcquire,
		State:         StateFailed,
		Limits:        Limits{MaxInputBytes: maxBytes},
	}
}

func failReport(report Report, state State, code FailureCode, message string) Report {
	report.State = state
	report.Failure = &Failure{Code: code, Message: message}
	report.Input = nil
	return report
}

func acquireSnapshot(
	ctx context.Context,
	inputPath string,
	scratchRoot string,
	report Report,
	afterFirstRead func(),
) (*Snapshot, Report) {
	if err := ctx.Err(); err != nil {
		return nil, failReport(report, StateCanceled, FailureCanceled, "document acquisition was canceled")
	}
	if strings.TrimSpace(inputPath) == "" || strings.TrimSpace(scratchRoot) == "" {
		return nil, failReport(report, StateFailed, FailureInvalidInput, "input and protected scratch are required")
	}

	source, sourceInfo, err := openRegularSource(inputPath)
	if err != nil {
		return nil, acquisitionFailure(report, err)
	}
	defer func() { _ = source.Close() }()
	if sourceInfo.Size() > report.Limits.MaxInputBytes {
		return nil, failReport(report, StateFailed, FailureLimitExceeded, "document exceeds the input byte limit")
	}

	operationDir, err := makeOperationDir(scratchRoot)
	if err != nil {
		return nil, failReport(report, StateFailed, FailureInternal, "protected scratch is unavailable")
	}
	snapshot := &Snapshot{path: filepath.Join(operationDir, "snapshot.pdf"), dir: operationDir}

	digest, size, err := copyAndHash(ctx, source, snapshot.path, report.Limits.MaxInputBytes)
	if err != nil {
		return cleanupAcquisitionFailure(snapshot, acquisitionFailure(report, err))
	}
	if afterFirstRead != nil {
		afterFirstRead()
	}
	stable, err := verifyStableSource(ctx, inputPath, source, sourceInfo, digest, size, report.Limits.MaxInputBytes)
	if err != nil {
		return cleanupAcquisitionFailure(snapshot, acquisitionFailure(report, err))
	}
	if !stable {
		return cleanupAcquisitionFailure(
			snapshot,
			failReport(report, StateFailed, FailureSourceChanged, "document changed during acquisition"),
		)
	}
	contentType, err := detectPDF(snapshot.path)
	if err != nil {
		return cleanupAcquisitionFailure(
			snapshot,
			failReport(report, StateFailed, FailureInternal, "immutable snapshot could not be verified"),
		)
	}
	if contentType == "" {
		return cleanupAcquisitionFailure(
			snapshot,
			failReport(report, StateUnsupported, FailureUnsupportedType, "input is not a PDF document"),
		)
	}
	if err := os.Chmod(snapshot.path, 0o400); err != nil {
		return cleanupAcquisitionFailure(
			snapshot,
			failReport(report, StateFailed, FailureInternal, "immutable snapshot could not be protected"),
		)
	}

	report.State = StateSucceeded
	report.Input = &DocumentRef{
		Ref:              "document://local/" + report.OperationID,
		OriginalFilename: filepath.Base(inputPath),
		ContentType:      contentType,
		Size:             size,
		SHA256:           digest,
		Authority:        Authority{Kind: "local_operator"},
		SourceKind:       "local_file",
		CreatedAt:        time.Now().UTC(),
		CleanupPolicy:    "delete_on_operation_close",
	}
	report.Failure = nil
	return snapshot, report
}

func cleanupAcquisitionFailure(snapshot *Snapshot, report Report) (*Snapshot, Report) {
	if err := snapshot.Close(); err != nil {
		return snapshot, failReport(
			report,
			StateFailed,
			FailureInternal,
			"protected scratch cleanup failed",
		)
	}
	return nil, report
}

type acquisitionError struct {
	code FailureCode
	err  error
}

func (e *acquisitionError) Error() string { return e.err.Error() }
func (e *acquisitionError) Unwrap() error { return e.err }

func acquisitionFailure(report Report, err error) Report {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return failReport(report, StateCanceled, FailureCanceled, "document acquisition was canceled")
	}
	var typed *acquisitionError
	if errors.As(err, &typed) {
		switch typed.code {
		case FailureInvalidInput:
			return failReport(report, StateFailed, typed.code, "input must be a direct regular file")
		case FailureLimitExceeded:
			return failReport(report, StateFailed, typed.code, "document exceeds the input byte limit")
		case FailureSourceChanged:
			return failReport(report, StateFailed, typed.code, "document changed during acquisition")
		}
	}
	return failReport(report, StateFailed, FailureInternal, "document acquisition failed")
}

func openRegularSource(path string) (*os.File, os.FileInfo, error) {
	return openRegularSourceWithHook(path, nil)
}

func openRegularSourceWithHook(path string, afterLstat func()) (*os.File, os.FileInfo, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, nil, &acquisitionError{code: FailureInvalidInput, err: err}
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return nil, nil, &acquisitionError{
			code: FailureInvalidInput,
			err:  fmt.Errorf("source is not a direct regular file"),
		}
	}
	if afterLstat != nil {
		afterLstat()
	}
	file, err := openSourceNoFollow(path)
	if err != nil {
		return nil, nil, &acquisitionError{code: FailureInvalidInput, err: err}
	}
	openedInfo, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, err
	}
	if !openedInfo.Mode().IsRegular() || !os.SameFile(info, openedInfo) {
		_ = file.Close()
		return nil, nil, &acquisitionError{
			code: FailureInvalidInput,
			err:  fmt.Errorf("source changed while opening"),
		}
	}
	return file, openedInfo, nil
}

func makeOperationDir(root string) (string, error) {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if mkdirErr := os.MkdirAll(absRoot, 0o700); mkdirErr != nil {
		return "", mkdirErr
	}
	info, err := os.Lstat(absRoot)
	if err != nil {
		return "", err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return "", fmt.Errorf("protected scratch is not a direct directory")
	}
	if chmodErr := os.Chmod(absRoot, 0o700); chmodErr != nil {
		return "", chmodErr
	}
	dir, err := os.MkdirTemp(absRoot, ".operation-")
	if err != nil {
		return "", err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

func copyAndHash(ctx context.Context, source *os.File, destination string, maxBytes int64) (string, int64, error) {
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", 0, err
	}
	remove := true
	defer func() {
		_ = output.Close()
		if remove {
			_ = os.Remove(destination)
		}
	}()
	hash := sha256.New()
	count, err := io.Copy(
		io.MultiWriter(output, hash),
		io.LimitReader(&contextReader{ctx: ctx, reader: source}, maxBytes+1),
	)
	if err != nil {
		return "", 0, err
	}
	if count > maxBytes {
		return "", 0, &acquisitionError{code: FailureLimitExceeded, err: fmt.Errorf("input exceeded limit")}
	}
	if err := output.Sync(); err != nil {
		return "", 0, err
	}
	if err := output.Close(); err != nil {
		return "", 0, err
	}
	remove = false
	return hex.EncodeToString(hash.Sum(nil)), count, nil
}

func verifyStableSource(
	ctx context.Context,
	path string,
	source *os.File,
	initial os.FileInfo,
	wantDigest string,
	wantSize int64,
	maxBytes int64,
) (bool, error) {
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return false, err
	}
	hash := sha256.New()
	count, err := io.Copy(hash, io.LimitReader(&contextReader{ctx: ctx, reader: source}, maxBytes+1))
	if err != nil {
		return false, err
	}
	if count > maxBytes {
		return false, &acquisitionError{code: FailureLimitExceeded, err: fmt.Errorf("input exceeded limit")}
	}
	final, err := source.Stat()
	if err != nil {
		return false, err
	}
	current, err := os.Lstat(path)
	if err != nil {
		return false, &acquisitionError{code: FailureSourceChanged, err: err}
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	return count == wantSize && digest == wantDigest &&
		initial.Size() == final.Size() && initial.ModTime().Equal(final.ModTime()) &&
		current.Mode()&os.ModeSymlink == 0 && os.SameFile(initial, final) && os.SameFile(final, current), nil
}

func detectPDF(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { _ = file.Close() }()
	buffer := make([]byte, pdfHeaderBytes)
	count, err := file.Read(buffer)
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	if !bytes.Contains(buffer[:count], []byte("%PDF-")) {
		return "", nil
	}
	return "application/pdf", nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r *contextReader) Read(buffer []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(buffer)
}
