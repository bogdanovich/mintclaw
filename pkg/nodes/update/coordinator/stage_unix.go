//go:build linux || darwin

package coordinator

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	nodeupdate "github.com/bogdanovich/mintclaw/pkg/nodes/update"
)

const (
	manifestAssetName  = "mintclaw-node-manifest.json"
	signatureAssetName = "mintclaw-node-manifest.sig"
)

func (coordinator *Coordinator) stageDownloaded(
	ctx context.Context,
	state State,
	authority ResolvedRelease,
) (State, error) {
	if err := ctx.Err(); err != nil {
		return coordinator.cancelStage(state)
	}
	if state.Transaction.Phase == PhaseVerified {
		reconciled, ok, reconcileErr := coordinator.reconcilePublishedCandidate(state)
		if reconcileErr != nil {
			return State{}, &StageError{Code: "state_unknown"}
		}
		if ok {
			return reconciled, nil
		}
	}
	manifestURL, err := releaseAssetURL(authority.BaseURL, authority.Tag, manifestAssetName)
	if err != nil {
		return coordinator.failStage(state, "source_invalid")
	}
	signatureURL, err := releaseAssetURL(authority.BaseURL, authority.Tag, signatureAssetName)
	if err != nil {
		return coordinator.failStage(state, "source_invalid")
	}
	manifestData, err := coordinator.fetchBounded(ctx, manifestURL, nodeupdate.MaxManifestBytes, authority)
	if err != nil {
		if ctx.Err() != nil {
			return coordinator.cancelStage(state)
		}
		return coordinator.failStage(state, "manifest_unavailable")
	}
	manifestDigest := sha256.Sum256(manifestData)
	if hex.EncodeToString(manifestDigest[:]) != state.Transaction.ManifestSHA256 {
		return coordinator.failStage(state, "manifest_changed")
	}
	signatureData, err := coordinator.fetchBounded(ctx, signatureURL, nodeupdate.MaxSignatureBytes, authority)
	if err != nil {
		if ctx.Err() != nil {
			return coordinator.cancelStage(state)
		}
		return coordinator.failStage(state, "manifest_unavailable")
	}
	manifest, err := nodeupdate.VerifyAt(
		manifestData,
		signatureData,
		authority.TrustedKey,
		coordinator.now().UTC(),
	)
	if err != nil || manifest.Release != authority.Tag || manifest.Channel != authority.Channel ||
		nodeupdate.CompareReleaseVersions(coordinator.version, manifest.MinimumCoordinatorVersion) < 0 {
		return coordinator.failStage(state, "manifest_untrusted")
	}
	artifact, found := manifestArtifact(
		manifest,
		state.Installation.Platform,
		state.Installation.Architecture,
	)
	if !found || artifact.SHA256 != state.Transaction.ArtifactSHA256 {
		return coordinator.failStage(state, "artifact_changed")
	}
	artifactURL, err := releaseAssetURL(authority.BaseURL, authority.Tag, artifact.Name)
	if err != nil {
		return coordinator.failStage(state, "source_invalid")
	}
	archiveName, err := coordinator.downloadArchive(ctx, artifactURL, artifact, authority)
	if err != nil {
		if ctx.Err() != nil {
			return coordinator.cancelStage(state)
		}
		return coordinator.failStage(state, "download_failed")
	}
	defer func() { _ = unix.Unlinkat(int(coordinator.store.root.Fd()), archiveName, 0) }()
	candidateSlot := SlotA
	if state.Active.Slot == SlotA {
		candidateSlot = SlotB
	}
	candidateName, candidate, err := coordinator.store.extractCandidate(
		archiveName,
		candidateSlot,
		manifest.Release,
		authority.Version,
		state.Installation,
	)
	if err != nil {
		return coordinator.failStage(state, "artifact_invalid")
	}
	removeCandidate := true
	defer func() {
		if removeCandidate {
			_ = unix.Unlinkat(int(coordinator.store.root.Fd()), candidateName, 0)
		}
	}()
	if err = coordinator.store.validatePlatformSignature(
		ctx,
		candidateName,
		authority.RequirePlatformSignature,
		state.Installation,
	); err != nil {
		return coordinator.failStage(state, "platform_signature_invalid")
	}
	if !coordinator.now().UTC().Before(timeFromUnix(state.Transaction.ExpiresAt)) {
		return coordinator.failStage(state, "request_expired")
	}
	if ctx.Err() != nil {
		return coordinator.cancelStage(state)
	}
	state.Generation++
	state.Transaction.Phase = PhaseVerified
	state.Transaction.Candidate = &candidate
	state.Transaction.UpdatedAt = coordinator.transitionTime(*state.Transaction)
	if err = coordinator.store.Commit(state.Generation-1, state); err != nil {
		return State{}, &StageError{Code: "state_unknown"}
	}
	if err = coordinator.store.publishCandidate(candidateName, candidateSlot); err != nil {
		return coordinator.failStage(state, "publication_failed")
	}
	removeCandidate = false
	if ctx.Err() != nil {
		return coordinator.cancelStage(state)
	}
	state.Generation++
	state.Transaction.Phase = PhaseStaged
	state.Transaction.UpdatedAt = coordinator.transitionTime(*state.Transaction)
	if err = coordinator.store.Commit(state.Generation-1, state); err != nil {
		return State{}, &StageError{Code: "state_unknown"}
	}
	return state, nil
}

func (coordinator *Coordinator) cancelStage(state State) (State, error) {
	state.Generation++
	state.Transaction.Canceled = true
	state.Transaction.CanceledAt = coordinator.transitionTime(*state.Transaction)
	state.Transaction.FailureCode = "canceled"
	state.Transaction.UpdatedAt = state.Transaction.CanceledAt
	if err := coordinator.store.Commit(state.Generation-1, state); err != nil {
		return State{}, &StageError{Code: "state_unknown"}
	}
	return state, nil
}

func (coordinator *Coordinator) reconcilePublishedCandidate(state State) (State, bool, error) {
	if state.Transaction == nil || state.Transaction.Candidate == nil {
		return state, false, nil
	}
	candidatePublished := coordinator.store.verifyPayload(*state.Transaction.Candidate, state.Installation) == nil
	if !candidatePublished {
		return state, false, nil
	}
	state.Generation++
	state.Transaction.Phase = PhaseStaged
	state.Transaction.UpdatedAt = min(coordinator.now().UTC().Unix(), state.Transaction.ExpiresAt)
	if err := coordinator.store.Commit(state.Generation-1, state); err != nil {
		return State{}, false, err
	}
	return state, true, nil
}

func releaseAssetURL(baseURL string, tag string, asset string) (string, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil || !nodeupdate.ValidReleaseVersion(tag) || asset == "" || strings.Contains(asset, "/") {
		return "", errors.New("invalid release asset identity")
	}
	parsed.Path = strings.TrimSuffix(parsed.Path, "/") + "/" + tag + "/" + asset
	return parsed.String(), nil
}

func (coordinator *Coordinator) fetchBounded(
	ctx context.Context,
	assetURL string,
	maximum int,
	authority ResolvedRelease,
) ([]byte, error) {
	response, err := coordinator.get(ctx, assetURL, authority)
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK || response.ContentLength > int64(maximum) {
		return nil, errors.New("release metadata response was rejected")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, int64(maximum)+1))
	if err != nil || len(data) > maximum ||
		(response.ContentLength >= 0 && int64(len(data)) != response.ContentLength) {
		return nil, errors.New("release metadata response was incomplete or oversized")
	}
	return data, nil
}

func (coordinator *Coordinator) get(
	ctx context.Context,
	assetURL string,
	authority ResolvedRelease,
) (*http.Response, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, assetURL, nil)
	if err != nil {
		return nil, errors.New("create release request")
	}
	request.Header.Set("Accept", "application/octet-stream")
	client := *coordinator.httpClient
	client.CheckRedirect = func(request *http.Request, via []*http.Request) error {
		if len(via) >= 3 || request.URL.Scheme != "https" || request.URL.User != nil ||
			!redirectHostAllowed(request.URL.Hostname(), authority) {
			return errors.New("release redirect was rejected")
		}
		request.Header = http.Header{"Accept": []string{"application/octet-stream"}}
		return nil
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, errors.New("release request failed")
	}
	return response, nil
}

func redirectHostAllowed(host string, authority ResolvedRelease) bool {
	base, err := url.Parse(authority.BaseURL)
	if err == nil && strings.EqualFold(host, base.Hostname()) {
		return true
	}
	host = strings.ToLower(host)
	for _, allowed := range authority.RedirectHosts {
		if host == allowed {
			return true
		}
	}
	return false
}

func manifestArtifact(manifest nodeupdate.Manifest, platform string, architecture string) (nodeupdate.Artifact, bool) {
	for _, artifact := range manifest.Artifacts {
		if artifact.Platform == platform && artifact.Architecture == architecture {
			return artifact, true
		}
	}
	return nodeupdate.Artifact{}, false
}

func (coordinator *Coordinator) downloadArchive(
	ctx context.Context,
	assetURL string,
	artifact nodeupdate.Artifact,
	authority ResolvedRelease,
) (string, error) {
	response, err := coordinator.get(ctx, assetURL, authority)
	if err != nil {
		return "", err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK || response.ContentLength > artifact.Size ||
		response.ContentLength > nodeupdate.MaxNodeArtifactBytes {
		return "", errors.New("release artifact response was rejected")
	}
	name, err := randomNamedTemporary("archive")
	if err != nil {
		return "", err
	}
	file, err := openFixedFileAt(
		int(coordinator.store.root.Fd()), name,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0o600,
	)
	if err != nil {
		return "", errors.New("create release artifact temporary")
	}
	remove := true
	defer func() {
		_ = file.Close()
		if remove {
			_ = unix.Unlinkat(int(coordinator.store.root.Fd()), name, 0)
		}
	}()
	digest := sha256.New()
	if err = coordinator.store.injectFault("archive_write"); err != nil {
		return "", err
	}
	written, err := io.Copy(io.MultiWriter(file, digest), io.LimitReader(response.Body, artifact.Size+1))
	if err != nil || written != artifact.Size || written > nodeupdate.MaxNodeArtifactBytes ||
		(response.ContentLength >= 0 && written != response.ContentLength) ||
		hex.EncodeToString(digest.Sum(nil)) != artifact.SHA256 {
		return "", errors.New("release artifact was incomplete or changed")
	}
	if err = file.Sync(); err != nil {
		return "", errors.New("sync release artifact")
	}
	if err = file.Close(); err != nil {
		return "", errors.New("close release artifact")
	}
	remove = false
	return name, nil
}

func (store *Store) extractCandidate(
	archiveName string,
	slot Slot,
	release string,
	version string,
	installation Installation,
) (string, Payload, error) {
	archive, err := openFixedFileAt(
		int(store.root.Fd()), archiveName, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0,
	)
	if err != nil {
		return "", Payload{}, err
	}
	defer func() { _ = archive.Close() }()
	archiveInfo, err := archive.Stat()
	if err != nil {
		return "", Payload{}, err
	}
	links, owner, ok := unixFileIdentity(archiveInfo)
	if !ok || !archiveInfo.Mode().IsRegular() || archiveInfo.Mode().Perm() != 0o600 || links != 1 ||
		owner != uint64(os.Geteuid()) {
		return "", Payload{}, errors.New("archive temporary identity is invalid")
	}
	compressed, err := gzip.NewReader(archive)
	if err != nil {
		return "", Payload{}, errors.New("candidate archive is not gzip")
	}
	defer func() { _ = compressed.Close() }()
	compressed.Multistream(false)
	reader := tar.NewReader(compressed)
	header, err := reader.Next()
	if err != nil || header.Name != "mintclaw-node" ||
		header.Typeflag != tar.TypeReg || header.Linkname != "" ||
		header.Size <= 0 || header.Size > MaxPayloadBytes || header.Mode&0o7000 != 0 || header.Mode&0o111 == 0 {
		return "", Payload{}, errors.New("candidate archive does not contain one bounded executable")
	}
	name, err := randomNamedTemporary("candidate")
	if err != nil {
		return "", Payload{}, err
	}
	candidate, err := openFixedFileAt(
		int(store.root.Fd()), name,
		unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0o600,
	)
	if err != nil {
		return "", Payload{}, err
	}
	remove := true
	defer func() {
		_ = candidate.Close()
		if remove {
			_ = unix.Unlinkat(int(store.root.Fd()), name, 0)
		}
	}()
	digest := sha256.New()
	written, err := io.Copy(io.MultiWriter(candidate, digest), io.LimitReader(reader, header.Size+1))
	if err != nil || written != header.Size {
		return "", Payload{}, errors.New("candidate executable was incomplete")
	}
	if _, err = reader.Next(); !errors.Is(err, io.EOF) {
		return "", Payload{}, errors.New("candidate archive contains additional entries")
	}
	if err = candidate.Sync(); err != nil {
		return "", Payload{}, errors.New("sync candidate executable")
	}
	if err = validateExecutableFile(candidate, installation.Platform, installation.Architecture); err != nil {
		return "", Payload{}, err
	}
	if err = candidate.Chmod(0o500); err != nil {
		return "", Payload{}, errors.New("make candidate executable")
	}
	if err = candidate.Sync(); err != nil {
		return "", Payload{}, errors.New("sync candidate executable mode")
	}
	if err = candidate.Close(); err != nil {
		return "", Payload{}, errors.New("close candidate executable")
	}
	remove = false
	return name, Payload{
		Slot: slot, Release: release, Version: version,
		SHA256: hex.EncodeToString(digest.Sum(nil)), Size: written,
	}, nil
}

func (store *Store) publishCandidate(temporaryName string, slot Slot) error {
	target, err := payloadFileName(slot)
	if err != nil {
		return err
	}
	if existing, openErr := openFixedFileAt(
		int(store.root.Fd()), target, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0,
	); openErr == nil {
		info, statErr := existing.Stat()
		_ = existing.Close()
		if statErr != nil {
			return errors.New("inspect inactive payload slot")
		}
		links, owner, ok := unixFileIdentity(info)
		if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0o500 || links != 1 ||
			owner != uint64(os.Geteuid()) {
			return errors.New("inactive payload slot identity is invalid")
		}
	} else if !errors.Is(openErr, os.ErrNotExist) {
		return errors.New("open inactive payload slot")
	}
	if err = unix.Renameat(int(store.root.Fd()), temporaryName, int(store.root.Fd()), target); err != nil {
		return errors.New("publish inactive payload slot")
	}
	if err = store.injectFault("candidate_after_publish"); err != nil {
		return err
	}
	return unix.Fsync(int(store.root.Fd()))
}

func (store *Store) verifyPayload(payload Payload, installation Installation) error {
	name, err := payloadFileName(payload.Slot)
	if err != nil {
		return err
	}
	file, err := openFixedFileAt(
		int(store.root.Fd()), name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0,
	)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return err
	}
	links, owner, ok := unixFileIdentity(info)
	if !ok || !info.Mode().IsRegular() || info.Mode().Perm() != 0o500 || links != 1 ||
		owner != uint64(os.Geteuid()) || info.Size() != payload.Size {
		return errors.New("payload slot identity changed")
	}
	if err = validateExecutableFile(file, installation.Platform, installation.Architecture); err != nil {
		return err
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return err
	}
	digest := sha256.New()
	written, err := io.Copy(digest, io.LimitReader(file, payload.Size+1))
	if err != nil || written != payload.Size || hex.EncodeToString(digest.Sum(nil)) != payload.SHA256 {
		return errors.New("payload slot content changed")
	}
	return nil
}

func randomNamedTemporary(kind string) (string, error) {
	name, err := randomTemporaryName()
	if err != nil {
		return "", err
	}
	return "." + kind + strings.TrimPrefix(name, ".state"), nil
}

func timeFromUnix(value int64) time.Time {
	return time.Unix(value, 0).UTC()
}
