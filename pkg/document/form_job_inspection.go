package document

import (
	"os"
	"path/filepath"
	"runtime"

	"golang.org/x/crypto/chacha20poly1305"
)

// InspectFormJob reads an existing owner-bound public record without initializing
// a store, taking file locks, expiring jobs, or writing keys/state. It reuses the
// native integrity and owner checks, and never decrypts field values. Callers
// must admit trusted local roots and check freshness separately; this observation
// is not approval authority or a safe model/transport projection.
func InspectFormJob(options FormJobStoreOptions, jobID string, owner FormJobOwner) (FormJobRecord, error) {
	options, err := normalizeFormJobStoreOptions(options)
	if err != nil {
		return FormJobRecord{}, err
	}
	directory := filepath.Join(options.StateRoot, "document_form_jobs")
	for _, path := range []string{options.StateRoot, options.KeyRoot, directory} {
		info, statErr := os.Lstat(path)
		if statErr != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
			runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
			return FormJobRecord{}, ErrFormJobStoreUnavailable
		}
	}
	key, err := readProtectedRegularFile(filepath.Join(options.KeyRoot, formJobKeyFileName), chacha20poly1305.KeySize)
	if err != nil || len(key) != chacha20poly1305.KeySize {
		clear(key)
		return FormJobRecord{}, ErrFormJobKeyUnavailable
	}
	defer clear(key)
	store := &FormJobStore{
		path: filepath.Join(directory, formJobStoreFileName), profileKey: key,
		maxJobs: options.MaxJobs, maxEventsPerJob: options.MaxEventsPerJob, maxBytes: options.MaxBytes,
	}
	document, err := store.loadLocked()
	if err != nil {
		return FormJobRecord{}, err
	}
	record, err := store.authorizedPublicRecord(&document, jobID, owner)
	if err != nil {
		return FormJobRecord{}, err
	}
	return cloneStoredFormJobPublic(record), nil
}
