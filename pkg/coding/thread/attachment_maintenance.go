package thread

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
)

const attachmentMaintenanceDirectory = "attachment-maintenance"

// attachmentMaintenanceLease serializes operations which can create a new
// blob-to-manifest edge with garbage collection and active-to-trash moves.
// Per-thread leases remain the authority for thread mutation.
type attachmentMaintenanceLease struct {
	storeRoot string
	root      *os.Root
	directory *os.Root
	file      *os.File
	once      sync.Once
	err       error
}

func (l *attachmentMaintenanceLease) Validate() error {
	if l == nil || l.root == nil || l.directory == nil || l.file == nil {
		return fmt.Errorf("coding attachment maintenance: lease is incomplete")
	}
	return validateAttachmentMaintenanceFile(l.storeRoot, l.root, l.directory, l.file)
}

func (l *attachmentMaintenanceLease) Release() error {
	if l == nil {
		return nil
	}
	l.once.Do(func() {
		l.err = errors.Join(
			releaseThreadLeaseFile(l.file),
			l.file.Close(),
			l.directory.Close(),
			l.root.Close(),
		)
	})
	return l.err
}

func (s *Store) acquireAttachmentMaintenanceLease() (*attachmentMaintenanceLease, error) {
	if s == nil {
		return nil, fmt.Errorf("coding attachment maintenance: store is nil")
	}
	root, err := openPinnedCatalogRoot(s.root)
	if err != nil {
		return nil, fmt.Errorf("coding attachment maintenance: pin store root: %w", err)
	}
	keepPinned := false
	var pinned *os.Root
	var file *os.File
	defer func() {
		if keepPinned {
			return
		}
		if file != nil {
			_ = file.Close()
		}
		if pinned != nil {
			_ = pinned.Close()
		}
		_ = root.Close()
	}()
	if prepareErr := ensureDirectTrashDirectory(root, attachmentMaintenanceDirectory); prepareErr != nil {
		return nil, fmt.Errorf("coding attachment maintenance: prepare lock directory: %w", prepareErr)
	}
	if syncErr := s.syncDir(s.root); syncErr != nil {
		return nil, fmt.Errorf("coding attachment maintenance: sync lock directory: %w", syncErr)
	}
	pinned, err = root.OpenRoot(attachmentMaintenanceDirectory)
	if err != nil {
		return nil, fmt.Errorf("coding attachment maintenance: pin lock directory: %w", err)
	}
	if validationErr := validatePinnedAttachmentDirectory(
		root,
		attachmentMaintenanceDirectory,
		pinned,
	); validationErr != nil {
		return nil, fmt.Errorf("coding attachment maintenance: validate lock directory: %w", validationErr)
	}
	directory, err := openCatalogRoot(filepath.Join(s.root, attachmentMaintenanceDirectory))
	if err != nil {
		return nil, fmt.Errorf("coding attachment maintenance: open lock directory: %w", err)
	}
	var openErr error
	file, openErr = openThreadLeaseFile(directory)
	closeErr := directory.Close()
	if err := errors.Join(openErr, closeErr); err != nil {
		return nil, fmt.Errorf("coding attachment maintenance: open lock file: %w", err)
	}
	if err := validateAttachmentMaintenanceFile(s.root, root, pinned, file); err != nil {
		return nil, err
	}
	if err := tryAcquireThreadLeaseFile(file); err != nil {
		if errors.Is(err, ErrLeaseBusy) {
			return nil, fmt.Errorf("coding attachment maintenance is busy: %w", ErrLeaseBusy)
		}
		return nil, fmt.Errorf("coding attachment maintenance: lock: %w", err)
	}
	if err := validateAttachmentMaintenanceFile(s.root, root, pinned, file); err != nil {
		_ = releaseThreadLeaseFile(file)
		return nil, err
	}
	if err := writeLeaseOwner(file, newLeaseOwner()); err != nil {
		_ = releaseThreadLeaseFile(file)
		return nil, fmt.Errorf("coding attachment maintenance: record owner: %w", err)
	}
	lease := &attachmentMaintenanceLease{
		storeRoot: s.root,
		root:      root,
		directory: pinned,
		file:      file,
	}
	if err := lease.Validate(); err != nil {
		_ = releaseThreadLeaseFile(file)
		return nil, err
	}
	keepPinned = true
	return lease, nil
}

func validateAttachmentMaintenanceFile(
	storeRoot string,
	pinnedStore *os.Root,
	pinnedDirectory *os.Root,
	locked *os.File,
) error {
	if err := validatePinnedAttachmentRoot(storeRoot, pinnedStore); err != nil {
		return fmt.Errorf("coding attachment maintenance: validate store root: %w", err)
	}
	if err := validatePinnedAttachmentDirectory(
		pinnedStore,
		attachmentMaintenanceDirectory,
		pinnedDirectory,
	); err != nil {
		return fmt.Errorf("coding attachment maintenance: validate lock directory: %w", err)
	}
	directory, err := openCatalogRoot(filepath.Join(storeRoot, attachmentMaintenanceDirectory))
	if err != nil {
		return fmt.Errorf("coding attachment maintenance: reopen lock directory: %w", err)
	}
	current, openErr := openThreadLeaseFile(directory)
	closeErr := directory.Close()
	if operationErr := errors.Join(openErr, closeErr); operationErr != nil {
		if current != nil {
			_ = current.Close()
		}
		return fmt.Errorf("coding attachment maintenance: reopen lock file: %w", operationErr)
	}
	defer func() { _ = current.Close() }()
	lockedInfo, err := locked.Stat()
	if err != nil {
		return err
	}
	currentInfo, err := current.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(lockedInfo, currentInfo) {
		return fmt.Errorf("coding attachment maintenance: locked file no longer identifies the active path")
	}
	return nil
}
