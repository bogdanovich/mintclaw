package nodes

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bogdanovich/mintclaw/pkg/fileutil"
)

func TestGatewayInvocationStorePersistsPreparedBinding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "node_invocations.json")
	store, err := NewGatewayInvocationStore(path, 8, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	plan := gatewayTestPlan(t, "inv_persist", "idem_persist", time.Now())
	record, created, err := store.Prepare(
		"vpn_box",
		"call-1",
		plan,
		gatewayTestDescriptor(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !created {
		t.Fatal("first prepare did not report record creation")
	}
	retained, created, err := store.Prepare(
		"vpn_box",
		"call-1",
		plan,
		gatewayTestDescriptor(),
	)
	if err != nil || created || retained.CreatedAt != record.CreatedAt {
		t.Fatalf("repeated prepare = (%#v, %v, %v)", retained, created, err)
	}
	if record.ExpectedPlanHash != plan.PlanHash ||
		record.State != GatewayInvocationPrepared {
		t.Fatalf("prepared record = %#v", record)
	}
	descriptorHash, err := record.Descriptor.Hash()
	if err != nil || descriptorHash != plan.DescriptorHash {
		t.Fatalf("prepared descriptor hash = %q, error %v", descriptorHash, err)
	}

	reloaded, err := NewGatewayInvocationStore(path, 8, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	got, found, err := reloaded.ByToolCall(gatewayTestPrincipal(plan), "call-1")
	if err != nil || !found || got.ExpectedPlanHash != plan.PlanHash ||
		got.Plan.InvocationID != plan.InvocationID {
		t.Fatalf("reloaded record = (%#v, %v, %v)", got, found, err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("store mode = %o", info.Mode().Perm())
	}
}

func TestGatewayInvocationRecordDescriptorValidationCache(t *testing.T) {
	store := newGatewayInvocationStore("", 8, 1024*1024, time.Now)
	firstPlan := gatewayTestPlan(t, "inv_cache_first", "idem_cache_first", time.Now())
	first, _, err := store.Prepare("vpn_box", "call-cache-first", firstPlan, gatewayTestDescriptor())
	if err != nil {
		t.Fatal(err)
	}
	secondPlan := gatewayTestPlan(t, "inv_cache_second", "idem_cache_second", time.Now())
	second, _, err := store.Prepare("vpn_box", "call-cache-second", secondPlan, gatewayTestDescriptor())
	if err != nil {
		t.Fatal(err)
	}

	cache := make(map[string][]CommandDescriptor)
	if err = first.validateWithDescriptorCache(cache); err != nil {
		t.Fatalf("validate first record: %v", err)
	}
	if err = second.validateWithDescriptorCache(cache); err != nil {
		t.Fatalf("validate repeated descriptor: %v", err)
	}
	if got := len(cache[first.Plan.DescriptorHash]); got != 1 {
		t.Fatalf("cached descriptor count = %d, want 1", got)
	}

	corrupt := second
	corrupt.Descriptor.OutputSchema = json.RawMessage(`{"type":"array"}`)
	if err = corrupt.validateWithDescriptorCache(cache); err == nil {
		t.Fatal("descriptor changed under a retained hash was accepted")
	}
	if got := len(cache[first.Plan.DescriptorHash]); got != 1 {
		t.Fatalf("cached descriptor count after rejection = %d, want 1", got)
	}
}

func TestGatewayInvocationStoreReloadsAcrossInstancesBeforeMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "node_invocations.json")
	first, err := NewGatewayInvocationStore(path, 8, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	second, err := NewGatewayInvocationStore(path, 8, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	firstPlan := gatewayTestPlan(t, "inv_first", "idem_first", time.Now())
	if _, _, err = first.Prepare(
		"vpn",
		"call-1",
		firstPlan,
		gatewayTestDescriptor(),
	); err != nil {
		t.Fatal(err)
	}
	secondPlan := gatewayTestPlan(t, "inv_second", "idem_second", time.Now())
	if _, _, err = second.Prepare(
		"vpn",
		"call-2",
		secondPlan,
		gatewayTestDescriptor(),
	); err != nil {
		t.Fatal(err)
	}
	for _, plan := range []ExecutionPlan{firstPlan, secondPlan} {
		if _, found, lookupErr := first.Lookup(
			gatewayTestPrincipal(plan),
			plan.InvocationID,
		); lookupErr != nil || !found {
			t.Fatalf("canonical record %q = (%v, %v)", plan.InvocationID, found, lookupErr)
		}
	}
	conflict := gatewayTestPlan(t, "inv_conflict", firstPlan.IdempotencyKey, time.Now())
	if _, _, err = second.Prepare(
		"vpn",
		"call-3",
		conflict,
		gatewayTestDescriptor(),
	); !errors.Is(
		err,
		ErrGatewayInvocationConflict,
	) {
		t.Fatalf("cross-instance idempotency conflict = %v", err)
	}
}

func TestGatewayInvocationStoreReusesValidatedUnchangedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "node_invocations.json")
	store, err := NewGatewayInvocationStore(path, 8, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	plan := gatewayTestPlan(t, "inv_cached_file", "idem_cached_file", time.Now())
	if _, _, err = store.Prepare(
		"vpn",
		"call-cached-file",
		plan,
		gatewayTestDescriptor(),
	); err != nil {
		t.Fatal(err)
	}

	reads := 0
	readFile := store.readFile
	store.readFile = func(
		path string,
		maxBytes int,
	) (gatewayInvocationDocument, *os.File, error) {
		reads++
		return readFile(path, maxBytes)
	}
	got, found, err := store.Lookup(gatewayTestPrincipal(plan), plan.InvocationID)
	if err != nil || !found {
		t.Fatalf("lookup cached record = (%#v, %v, %v)", got, found, err)
	}
	if reads != 0 {
		t.Fatalf("unchanged file reads = %d, want 0", reads)
	}
}

func TestGatewayInvocationStoreCachesIdentityOfValidatedFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	path := filepath.Join(dir, "node_invocations.json")
	store, err := NewGatewayInvocationStore(path, 8, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	firstPlan := gatewayTestPlan(t, "inv_identity_first", "idem_identity_first", time.Now())
	if _, _, err = store.Prepare(
		"vpn",
		"call-identity-first",
		firstPlan,
		gatewayTestDescriptor(),
	); err != nil {
		t.Fatal(err)
	}

	secondPath := filepath.Join(dir, "second.json")
	copyGatewayInvocationStoreFile(t, path, secondPath)
	secondStore, err := NewGatewayInvocationStore(secondPath, 8, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	secondPlan := gatewayTestPlan(t, "inv_identity_second", "idem_identity_second", time.Now())
	if _, _, err = secondStore.Prepare(
		"vpn",
		"call-identity-second",
		secondPlan,
		gatewayTestDescriptor(),
	); err != nil {
		t.Fatal(err)
	}

	thirdPath := filepath.Join(dir, "third.json")
	copyGatewayInvocationStoreFile(t, secondPath, thirdPath)
	thirdStore, err := NewGatewayInvocationStore(thirdPath, 8, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	thirdPlan := gatewayTestPlan(t, "inv_identity_third", "idem_identity_third", time.Now())
	if _, _, err = thirdStore.Prepare(
		"vpn",
		"call-identity-third",
		thirdPlan,
		gatewayTestDescriptor(),
	); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(secondPath, path); err != nil {
		t.Fatal(err)
	}

	replaced := false
	readFile := store.readFile
	store.readFile = func(
		path string,
		maxBytes int,
	) (gatewayInvocationDocument, *os.File, error) {
		document, file, readErr := readFile(path, maxBytes)
		if readErr == nil && !replaced {
			replaced = true
			if renameErr := os.Rename(thirdPath, path); renameErr != nil {
				_ = file.Close()
				return gatewayInvocationDocument{}, nil, renameErr
			}
		}
		return document, file, readErr
	}
	if _, found, lookupErr := store.Lookup(
		gatewayTestPrincipal(firstPlan),
		firstPlan.InvocationID,
	); lookupErr != nil || !found {
		t.Fatalf("lookup during replacement = (%v, %v)", found, lookupErr)
	}
	if _, found, lookupErr := store.Lookup(
		gatewayTestPrincipal(thirdPlan),
		thirdPlan.InvocationID,
	); lookupErr != nil || !found {
		t.Fatalf("lookup after replacement = (%v, %v)", found, lookupErr)
	}
}

func TestGatewayInvocationStoreBlocksDispatchAfterReplacementFollowingRead(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	path := filepath.Join(dir, "node_invocations.json")
	store, err := NewGatewayInvocationStore(path, 8, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	initialPlan := gatewayTestPlan(t, "inv_after_read_initial", "idem_after_read_initial", time.Now())
	if _, _, err = store.Prepare(
		"vpn", "call-after-read-initial", initialPlan, gatewayTestDescriptor(),
	); err != nil {
		t.Fatal(err)
	}

	preparedPath := filepath.Join(dir, "prepared.json")
	preparedStore, err := NewGatewayInvocationStore(preparedPath, 8, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	preparedPlan := gatewayTestPlan(t, "inv_after_read_dispatch", "idem_after_read_dispatch", time.Now())
	if _, _, err = preparedStore.Prepare(
		"vpn", "call-after-read-dispatch", preparedPlan, gatewayTestDescriptor(),
	); err != nil {
		t.Fatal(err)
	}
	canonicalPath := filepath.Join(dir, "canonical.json")
	canonicalStore, err := NewGatewayInvocationStore(canonicalPath, 8, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	canonicalPlan := gatewayTestPlan(t, "inv_after_read_canonical", "idem_after_read_canonical", time.Now())
	if _, _, err = canonicalStore.Prepare(
		"vpn", "call-after-read-canonical", canonicalPlan, gatewayTestDescriptor(),
	); err != nil {
		t.Fatal(err)
	}
	canonicalBytes, err := os.ReadFile(canonicalPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(preparedPath, path); err != nil {
		t.Fatal(err)
	}
	if err = preparedStore.Close(); err != nil {
		t.Fatal(err)
	}

	readFile := store.readFile
	store.readFile = func(
		path string,
		maxBytes int,
	) (gatewayInvocationDocument, *os.File, error) {
		document, file, readErr := readFile(path, maxBytes)
		if readErr == nil {
			if renameErr := os.Rename(canonicalPath, path); renameErr != nil {
				_ = file.Close()
				return gatewayInvocationDocument{}, nil, renameErr
			}
			if closeErr := canonicalStore.Close(); closeErr != nil {
				_ = file.Close()
				return gatewayInvocationDocument{}, nil, closeErr
			}
		}
		return document, file, readErr
	}
	_, transitioned, dispatchErr := store.MarkDispatched(
		gatewayTestOwner("vpn", "call-after-read-dispatch", preparedPlan),
		preparedPlan.InvocationID,
		preparedPlan.PlanHash,
	)
	if transitioned || !errors.Is(dispatchErr, ErrGatewayInvocationConflict) {
		t.Fatalf("dispatch after replacement = (%v, %v)", transitioned, dispatchErr)
	}
	actual, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, canonicalBytes) {
		t.Fatal("failed dispatch overwrote the canonical replacement ledger")
	}
	reloaded, err := NewGatewayInvocationStore(path, 8, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = reloaded.Close() }()
	if _, found, lookupErr := reloaded.Lookup(
		gatewayTestPrincipal(preparedPlan), preparedPlan.InvocationID,
	); lookupErr != nil || found {
		t.Fatalf("reloaded stale dispatch authority = (%v, %v)", found, lookupErr)
	}
}

func TestGatewayInvocationStoreBlocksPruneAfterReplacementFollowingRead(t *testing.T) {
	now := time.Now()
	dir := filepath.Join(t.TempDir(), "state")
	path := filepath.Join(dir, "node_invocations.json")
	store, err := NewGatewayInvocationStore(path, 8, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	store.now = func() time.Time { return now.Add(2 * time.Minute) }

	expiredPath := filepath.Join(dir, "expired.json")
	expiredStore, err := NewGatewayInvocationStore(expiredPath, 8, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	expiredPlan := gatewayTestPlan(t, "inv_after_read_expired", "idem_after_read_expired", now)
	if _, _, err = expiredStore.Prepare(
		"vpn", "call-after-read-expired", expiredPlan, gatewayTestDescriptor(),
	); err != nil {
		t.Fatal(err)
	}
	canonicalPath := filepath.Join(dir, "canonical-prune.json")
	canonicalStore, err := NewGatewayInvocationStore(canonicalPath, 8, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	canonicalPlan := gatewayTestPlan(t, "inv_after_read_retained", "idem_after_read_retained", now)
	if _, _, err = canonicalStore.Prepare(
		"vpn", "call-after-read-retained", canonicalPlan, gatewayTestDescriptor(),
	); err != nil {
		t.Fatal(err)
	}
	canonicalBytes, err := os.ReadFile(canonicalPath)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(expiredPath, path); err != nil {
		t.Fatal(err)
	}
	if err = expiredStore.Close(); err != nil {
		t.Fatal(err)
	}

	readFile := store.readFile
	store.readFile = func(
		path string,
		maxBytes int,
	) (gatewayInvocationDocument, *os.File, error) {
		document, file, readErr := readFile(path, maxBytes)
		if readErr == nil {
			if renameErr := os.Rename(canonicalPath, path); renameErr != nil {
				_ = file.Close()
				return gatewayInvocationDocument{}, nil, renameErr
			}
			if closeErr := canonicalStore.Close(); closeErr != nil {
				_ = file.Close()
				return gatewayInvocationDocument{}, nil, closeErr
			}
		}
		return document, file, readErr
	}
	if _, _, lookupErr := store.Lookup(
		gatewayTestPrincipal(expiredPlan), expiredPlan.InvocationID,
	); !errors.Is(lookupErr, ErrGatewayInvocationConflict) {
		t.Fatalf("prune after replacement error = %v", lookupErr)
	}
	actual, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(actual, canonicalBytes) {
		t.Fatal("failed prune overwrote the canonical replacement ledger")
	}
}

func TestGatewayInvocationStorePinsValidatedIdentityAcrossReplacementCycle(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	path := filepath.Join(dir, "node_invocations.json")
	store, err := NewGatewayInvocationStore(path, 8, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	firstPlan := gatewayTestPlan(t, "inv_cycle_firstx", "idem_cycle_firstx", time.Now())
	if _, _, err = store.Prepare(
		"vpn",
		"call-cycle-firstx",
		firstPlan,
		gatewayTestDescriptor(),
	); err != nil {
		t.Fatal(err)
	}
	validatedFile := store.file
	validatedInfo, err := validatedFile.Stat()
	if err != nil {
		t.Fatal(err)
	}

	for index, name := range []string{"middle", "finalx"} {
		replacementPath := filepath.Join(dir, name+".json")
		replacement, createErr := NewGatewayInvocationStore(replacementPath, 8, 1024*1024)
		if createErr != nil {
			t.Fatal(createErr)
		}
		plan := gatewayTestPlan(
			t,
			"inv_cycle_"+name,
			"idem_cycle_"+name,
			time.Now(),
		)
		if _, _, createErr = replacement.Prepare(
			"vpn",
			"call-cycle-"+name,
			plan,
			gatewayTestDescriptor(),
		); createErr != nil {
			t.Fatal(createErr)
		}
		if index == 1 {
			data, readErr := os.ReadFile(replacementPath)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if int64(len(data)) != validatedInfo.Size() {
				t.Fatalf(
					"final replacement size = %d, want cached size %d",
					len(data),
					validatedInfo.Size(),
				)
			}
			if chtimesErr := os.Chtimes(
				replacementPath,
				validatedInfo.ModTime(),
				validatedInfo.ModTime(),
			); chtimesErr != nil {
				t.Fatal(chtimesErr)
			}
		}
		if renameErr := os.Rename(replacementPath, path); renameErr != nil {
			t.Fatal(renameErr)
		}
	}

	finalInfo, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if finalInfo.Size() != validatedInfo.Size() ||
		!finalInfo.ModTime().Equal(validatedInfo.ModTime()) {
		t.Fatalf("final replacement metadata = %#v, want cached size and mtime", finalInfo)
	}
	finalPlan := gatewayTestPlan(t, "inv_cycle_finalx", "idem_cycle_finalx", time.Now())
	if _, found, lookupErr := store.Lookup(
		gatewayTestPrincipal(finalPlan),
		finalPlan.InvocationID,
	); lookupErr != nil || !found {
		t.Fatalf("lookup after replacement cycle = (%v, %v)", found, lookupErr)
	}
	if _, found, lookupErr := store.Lookup(
		gatewayTestPrincipal(firstPlan),
		firstPlan.InvocationID,
	); lookupErr != nil || found {
		t.Fatalf("stale lookup after replacement cycle = (%v, %v)", found, lookupErr)
	}
	if _, statErr := validatedFile.Stat(); statErr == nil {
		t.Fatal("superseded validated file handle remains open")
	}
}

func TestGatewayInvocationStoreRejectsOversizedOpenedReplacement(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	path := filepath.Join(dir, "node_invocations.json")
	const maxBytes = 4096
	store, err := NewGatewayInvocationStore(path, 8, maxBytes)
	if err != nil {
		t.Fatal(err)
	}
	firstPlan := gatewayTestPlan(t, "inv_size_first", "idem_size_first", time.Now())
	if _, _, err = store.Prepare(
		"vpn",
		"call-size-first",
		firstPlan,
		gatewayTestDescriptor(),
	); err != nil {
		t.Fatal(err)
	}

	replacementPath := filepath.Join(dir, "replacement.json")
	replacement, err := NewGatewayInvocationStore(replacementPath, 8, maxBytes)
	if err != nil {
		t.Fatal(err)
	}
	replacementPlan := gatewayTestPlan(t, "inv_size_second", "idem_size_second", time.Now())
	if _, _, err = replacement.Prepare(
		"vpn",
		"call-size-second",
		replacementPlan,
		gatewayTestDescriptor(),
	); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(replacementPath)
	if err != nil {
		t.Fatal(err)
	}
	oversizedPath := filepath.Join(dir, "oversized.json")
	oversized := append(append([]byte(nil), data...), bytes.Repeat([]byte(" "), maxBytes)...)
	if err = os.WriteFile(oversizedPath, oversized, 0o600); err != nil {
		t.Fatal(err)
	}
	if err = os.Rename(replacementPath, path); err != nil {
		t.Fatal(err)
	}

	readFile := store.readFile
	store.readFile = func(
		path string,
		maxBytes int,
	) (gatewayInvocationDocument, *os.File, error) {
		if renameErr := os.Rename(oversizedPath, path); renameErr != nil {
			return gatewayInvocationDocument{}, nil, renameErr
		}
		return readFile(path, maxBytes)
	}
	if _, _, lookupErr := store.Lookup(
		gatewayTestPrincipal(replacementPlan),
		replacementPlan.InvocationID,
	); !errors.Is(lookupErr, ErrGatewayInvocationStoreFull) {
		t.Fatalf("oversized opened replacement error = %v", lookupErr)
	}
}

func TestGatewayInvocationStoreDefaultCapacityExceedsLegacyLimit(t *testing.T) {
	store := newGatewayInvocationStore("", 0, 0, time.Now)
	now := time.Now()
	for index := range 257 {
		invocationID := fmt.Sprintf("inv_default_capacity_%d", index)
		plan := gatewayTestPlan(
			t,
			invocationID,
			fmt.Sprintf("idem_default_capacity_%d", index),
			now,
		)
		if _, _, err := store.Prepare(
			"vpn",
			fmt.Sprintf("call-default-capacity-%d", index),
			plan,
			gatewayTestDescriptor(),
		); err != nil {
			t.Fatalf("Prepare() invocation %d error = %v", index, err)
		}
	}
}

func TestGatewayInvocationStoreCloseReleasesPinnedIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "node_invocations.json")
	store, err := NewGatewayInvocationStore(path, 8, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	plan := gatewayTestPlan(t, "inv_close", "idem_close", time.Now())
	if _, _, err = store.Prepare("vpn", "call-close", plan, gatewayTestDescriptor()); err != nil {
		t.Fatal(err)
	}
	retained := store.file
	if err = store.Close(); err != nil {
		t.Fatal(err)
	}
	if err = store.Close(); err != nil {
		t.Fatalf("second Close() error = %v", err)
	}
	if _, statErr := retained.Stat(); statErr == nil {
		t.Fatal("Close() retained the validated file handle")
	}
	if _, _, lookupErr := store.Lookup(
		gatewayTestPrincipal(plan),
		plan.InvocationID,
	); !errors.Is(lookupErr, os.ErrClosed) {
		t.Fatalf("lookup after Close() error = %v", lookupErr)
	}
}

func TestGatewayInvocationStoreDoesNotCacheReplacedPostWriteFile(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	path := filepath.Join(dir, "node_invocations.json")
	store, err := NewGatewayInvocationStore(path, 8, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	replacement := filepath.Join(dir, "replacement.json")
	if err = os.WriteFile(replacement, []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	writeFile := store.writeFile
	store.writeFile = func(path string, data []byte, mode os.FileMode) error {
		if writeErr := writeFile(path, data, mode); writeErr != nil {
			return writeErr
		}
		return os.Rename(replacement, path)
	}
	plan := gatewayTestPlan(t, "inv_post_write_replace", "idem_post_write_replace", time.Now())
	_, _, err = store.Prepare(
		"vpn",
		"call-post-write-replace",
		plan,
		gatewayTestDescriptor(),
	)
	if err == nil || fileutil.IsCommittedWriteError(err) {
		t.Fatalf("post-write replacement error = %v, want uncommitted verification failure", err)
	}
	if store.loaded {
		t.Fatal("post-write replacement was cached as a validated file")
	}
	if _, retained := store.records[plan.InvocationID]; retained {
		t.Fatal("unverified post-write mutation remained in memory")
	}
}

func copyGatewayInvocationStoreFile(t *testing.T, source, destination string) {
	t.Helper()
	data, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(destination, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestGatewayInvocationStoreRejectsToolCallRebinding(t *testing.T) {
	store := newGatewayInvocationStore("", 8, 1024*1024, time.Now)
	first := gatewayTestPlan(t, "inv_first", "idem_first", time.Now())
	if _, _, err := store.Prepare(
		"vpn",
		"call-1",
		first,
		gatewayTestDescriptor(),
	); err != nil {
		t.Fatal(err)
	}
	second := gatewayTestPlan(t, "inv_second", "idem_second", time.Now())
	if _, _, err := store.Prepare(
		"vpn",
		"call-1",
		second,
		gatewayTestDescriptor(),
	); !errors.Is(
		err,
		ErrGatewayInvocationConflict,
	) {
		t.Fatalf("Prepare() error = %v", err)
	}
	if _, _, err := store.Prepare(
		"other",
		"call-2",
		first,
		gatewayTestDescriptor(),
	); !errors.Is(
		err,
		ErrGatewayInvocationConflict,
	) {
		t.Fatalf("invocation retry error = %v", err)
	}
	reusedKey := gatewayTestPlan(t, "inv_other", first.IdempotencyKey, time.Now())
	if _, _, err := store.Prepare(
		"vpn",
		"call-2",
		reusedKey,
		gatewayTestDescriptor(),
	); !errors.Is(
		err,
		ErrGatewayInvocationConflict,
	) {
		t.Fatalf("idempotency retry error = %v", err)
	}
}

func TestGatewayInvocationStoreMarksDispatchAgainstRetainedHash(t *testing.T) {
	store := newGatewayInvocationStore("", 8, 1024*1024, time.Now)
	plan := gatewayTestPlan(t, "inv_dispatch", "idem_dispatch", time.Now())
	if _, _, err := store.Prepare(
		"vpn",
		"call-1",
		plan,
		gatewayTestDescriptor(),
	); err != nil {
		t.Fatal(err)
	}
	owner := gatewayTestOwner("vpn", "call-1", plan)
	wrongOwner := owner
	wrongOwner.ToolCallID = "call-other"
	if _, _, err := store.MarkDispatched(
		wrongOwner,
		plan.InvocationID,
		plan.PlanHash,
	); !errors.Is(err, ErrGatewayInvocationConflict) {
		t.Fatalf("wrong owner error = %v", err)
	}
	if _, _, err := store.MarkDispatched(owner, plan.InvocationID, "wrong"); !errors.Is(
		err,
		ErrGatewayInvocationConflict,
	) {
		t.Fatalf("wrong hash error = %v", err)
	}
	dispatched, transitioned, err := store.MarkDispatched(
		owner,
		plan.InvocationID,
		plan.PlanHash,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !transitioned || dispatched.State != GatewayInvocationDispatched ||
		dispatched.DispatchedAt == 0 {
		t.Fatalf("dispatched record = %#v", dispatched)
	}
	principal := gatewayTestPrincipal(plan)
	principal.AgentID = "other"
	if _, found, lookupErr := store.Lookup(
		principal,
		plan.InvocationID,
	); lookupErr != nil || found {
		t.Fatal("different agent accessed invocation")
	}
	principal = gatewayTestPrincipal(plan)
	principal.SessionID = "other"
	if _, found, lookupErr := store.Lookup(
		principal,
		plan.InvocationID,
	); lookupErr != nil || found {
		t.Fatal("different session accessed invocation")
	}
	principal = gatewayTestPrincipal(plan)
	principal.ActorID = "other"
	if _, found, lookupErr := store.Lookup(
		principal,
		plan.InvocationID,
	); lookupErr != nil || found {
		t.Fatal("different actor accessed invocation")
	}
	if _, found, lookupErr := store.Lookup(
		gatewayTestPrincipal(plan),
		plan.InvocationID,
	); lookupErr != nil || !found {
		t.Fatal("invocation owner could not access record")
	}
	repeated, transitioned, err := store.MarkDispatched(
		owner,
		plan.InvocationID,
		plan.PlanHash,
	)
	if err != nil || transitioned || repeated.DispatchedAt != dispatched.DispatchedAt {
		t.Fatalf("repeated dispatch = (%#v, %v, %v)", repeated, transitioned, err)
	}
}

func TestGatewayInvocationStorePersistsOneExactScopeCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state", "node_invocations.json")
	store, err := NewGatewayInvocationStore(path, 8, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	plan := gatewayTestPlan(t, "inv_cancel", "idem_cancel", time.Now())
	principal := gatewayTestPrincipal(plan)
	principal.WorkspaceID = "workspace_1"
	principal.ExecutionID = "execution_1"
	record, _, err := store.PrepareOwned(
		principal,
		"vpn",
		"call-1",
		plan,
		gatewayTestDescriptor(),
	)
	if err != nil {
		t.Fatal(err)
	}
	owner := gatewayTestOwner("vpn", "call-1", plan)
	owner.WorkspaceID = principal.WorkspaceID
	owner.ExecutionID = principal.ExecutionID
	if _, _, err := store.MarkDispatched(owner, plan.InvocationID, plan.PlanHash); err != nil {
		t.Fatal(err)
	}

	wrongExecution := principal
	wrongExecution.ExecutionID = "execution_2"
	if _, transitioned, err := store.RequestCancellation(
		wrongExecution,
		plan.InvocationID,
	); !errors.Is(err, ErrGatewayInvocationConflict) || transitioned {
		t.Fatalf("wrong execution cancellation = transitioned %v, error %v", transitioned, err)
	}
	requested, transitioned, err := store.RequestCancellation(principal, plan.InvocationID)
	if err != nil || !transitioned || requested.Cancellation == nil {
		t.Fatalf("first cancellation = (%#v, %v, %v)", requested, transitioned, err)
	}
	repeated, transitioned, err := store.RequestCancellation(principal, plan.InvocationID)
	if err != nil || transitioned ||
		repeated.Cancellation.RequestedAt != requested.Cancellation.RequestedAt {
		t.Fatalf("repeated cancellation = (%#v, %v, %v)", repeated, transitioned, err)
	}
	reloaded, err := NewGatewayInvocationStore(path, 8, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	retained, found, err := reloaded.Lookup(principal, plan.InvocationID)
	if err != nil || !found || retained.Cancellation == nil ||
		retained.Cancellation.RequestedAt != requested.Cancellation.RequestedAt ||
		retained.WorkspaceID != record.WorkspaceID {
		t.Fatalf("reloaded cancellation = (%#v, %v, %v)", retained, found, err)
	}
}

func TestGatewayInvocationStoreCancellationFailsClosedForLegacyOwnership(t *testing.T) {
	store := newGatewayInvocationStore("", 8, 1024*1024, time.Now)
	plan := gatewayTestPlan(t, "inv_legacy", "idem_legacy", time.Now())
	if _, _, err := store.Prepare("vpn", "call-1", plan, gatewayTestDescriptor()); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.MarkDispatched(
		gatewayTestOwner("vpn", "call-1", plan),
		plan.InvocationID,
		plan.PlanHash,
	); err != nil {
		t.Fatal(err)
	}
	principal := gatewayTestPrincipal(plan)
	principal.WorkspaceID = "workspace_1"
	principal.ExecutionID = "execution_1"
	if _, found, err := store.Lookup(principal, plan.InvocationID); err != nil || !found {
		t.Fatalf("legacy status lookup = (%v, %v)", found, err)
	}
	if _, _, err := store.RequestCancellation(
		principal,
		plan.InvocationID,
	); !errors.Is(err, ErrGatewayInvocationConflict) {
		t.Fatalf("legacy cancellation error = %v", err)
	}
}

func TestGatewayInvocationStoreAllowsOneDispatchWinnerAcrossInstances(t *testing.T) {
	now := time.Now()
	path := filepath.Join(t.TempDir(), "node_invocations.json")
	first := newGatewayInvocationStore(path, 8, 1024*1024, func() time.Time { return now })
	second, err := NewGatewayInvocationStore(path, 8, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	second.now = func() time.Time { return now }
	plan := gatewayTestPlan(t, "inv_race", "idem_race", now)
	prepared, _, err := first.Prepare("vpn", "call-1", plan, gatewayTestDescriptor())
	if err != nil {
		t.Fatal(err)
	}
	owner := gatewayTestOwner("vpn", "call-1", plan)
	type dispatchResult struct {
		record       GatewayInvocationRecord
		transitioned bool
		err          error
	}
	start := make(chan struct{})
	results := make(chan dispatchResult, 2)
	for _, store := range []*GatewayInvocationStore{first, second} {
		go func() {
			<-start
			record, transitioned, dispatchErr := store.MarkDispatched(
				owner,
				plan.InvocationID,
				plan.PlanHash,
			)
			results <- dispatchResult{
				record: record, transitioned: transitioned, err: dispatchErr,
			}
		}()
	}
	close(start)
	winners := 0
	for range 2 {
		result := <-results
		if result.err != nil {
			t.Fatal(result.err)
		}
		if result.transitioned {
			winners++
		}
		if result.record.UpdatedAt <= prepared.UpdatedAt {
			t.Fatalf("non-monotonic transition = %#v after %#v", result.record, prepared)
		}
	}
	if winners != 1 {
		t.Fatalf("dispatch transition winners = %d", winners)
	}
}

func TestGatewayInvocationStorePinsDescriptor(t *testing.T) {
	store := newGatewayInvocationStore("", 8, 1024*1024, time.Now)
	plan := gatewayTestPlan(t, "inv_descriptor", "idem_descriptor", time.Now())
	descriptor := gatewayTestDescriptor()
	record, _, err := store.Prepare("vpn", "call-1", plan, descriptor)
	if err != nil {
		t.Fatal(err)
	}
	descriptor.OutputSchema[0] = 'x'
	record.Descriptor.OutputSchema[0] = 'y'
	got, found, err := store.Lookup(gatewayTestPrincipal(plan), plan.InvocationID)
	if err != nil || !found {
		t.Fatalf("Lookup() = (%#v, %v, %v)", got, found, err)
	}
	hash, err := got.Descriptor.Hash()
	if err != nil || hash != plan.DescriptorHash {
		t.Fatalf("retained descriptor hash = %q, error %v", hash, err)
	}
	wrong := gatewayTestDescriptor()
	wrong.Risk = RiskRead
	other := gatewayTestPlan(t, "inv_wrong_descriptor", "idem_wrong_descriptor", time.Now())
	if _, _, err := store.Prepare("vpn", "call-2", other, wrong); !errors.Is(
		err,
		ErrInvalidInvocation,
	) {
		t.Fatalf("mismatched descriptor error = %v", err)
	}
}

func TestGatewayInvocationStoreRejectsExpiredPreparedAuthority(t *testing.T) {
	now := time.Now()
	store := newGatewayInvocationStore("", 8, 1024*1024, func() time.Time { return now })
	plan := gatewayTestPlan(t, "inv_expired", "idem_expired", now)
	if _, _, err := store.Prepare(
		"vpn",
		"call-1",
		plan,
		gatewayTestDescriptor(),
	); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if _, found, err := store.ByToolCall(
		gatewayTestPrincipal(plan),
		"call-1",
	); err != nil || found {
		t.Fatalf("expired ByToolCall() = (%v, %v)", found, err)
	}
	owner := gatewayTestOwner("vpn", "call-1", plan)
	if _, _, err := store.MarkDispatched(
		owner,
		plan.InvocationID,
		plan.PlanHash,
	); !errors.Is(err, ErrGatewayInvocationNotFound) {
		t.Fatalf("expired MarkDispatched() error = %v", err)
	}
}

func TestGatewayInvocationStoreKeepsCommittedMutationInMemory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node_invocations.json")
	store := newGatewayInvocationStore(path, 8, 1024*1024, time.Now)
	store.writeFile = func(path string, data []byte, mode os.FileMode) error {
		if err := os.WriteFile(path, data, mode); err != nil {
			return err
		}
		return &fileutil.CommittedWriteError{Err: errors.New("sync directory")}
	}
	plan := gatewayTestPlan(t, "inv_committed", "idem_committed", time.Now())
	if _, _, err := store.Prepare(
		"vpn",
		"call-1",
		plan,
		gatewayTestDescriptor(),
	); err == nil ||
		!fileutil.IsCommittedWriteError(err) {
		t.Fatalf("Prepare() error = %v", err)
	}
	got, found, err := store.ByToolCall(gatewayTestPrincipal(plan), "call-1")
	if err != nil || !found || got.Plan.InvocationID != plan.InvocationID {
		t.Fatalf("committed record = (%#v, %v, %v)", got, found, err)
	}
	owner := gatewayTestOwner("vpn", "call-1", plan)
	_, transitioned, dispatchErr := store.MarkDispatched(
		owner,
		plan.InvocationID,
		plan.PlanHash,
	)
	if !transitioned || dispatchErr == nil ||
		!fileutil.IsCommittedWriteError(dispatchErr) {
		t.Fatalf("MarkDispatched() error = %v", dispatchErr)
	}
	got, found, err = store.Lookup(gatewayTestPrincipal(plan), plan.InvocationID)
	if err != nil || !found || got.State != GatewayInvocationDispatched {
		t.Fatalf("committed dispatch = (%#v, %v, %v)", got, found, err)
	}
}

func TestGatewayInvocationStoreDoesNotGrantRolledBackDispatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node_invocations.json")
	store, err := NewGatewayInvocationStore(path, 8, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	plan := gatewayTestPlan(t, "inv_rollback", "idem_rollback", time.Now())
	if _, _, err = store.Prepare(
		"vpn",
		"call-1",
		plan,
		gatewayTestDescriptor(),
	); err != nil {
		t.Fatal(err)
	}
	writeErr := errors.New("write invocation store")
	store.writeFile = func(string, []byte, os.FileMode) error { return writeErr }
	owner := gatewayTestOwner("vpn", "call-1", plan)
	_, transitioned, err := store.MarkDispatched(
		owner,
		plan.InvocationID,
		plan.PlanHash,
	)
	if !errors.Is(err, writeErr) || transitioned {
		t.Fatalf("failed dispatch = (transitioned %v, error %v)", transitioned, err)
	}
	record, found, err := store.Lookup(gatewayTestPrincipal(plan), plan.InvocationID)
	if err != nil || !found || record.State != GatewayInvocationPrepared {
		t.Fatalf("rolled-back record = (%#v, %v, %v)", record, found, err)
	}

	store.writeFile = fileutil.WriteFileAtomic
	_, transitioned, err = store.MarkDispatched(
		owner,
		plan.InvocationID,
		plan.PlanHash,
	)
	if err != nil || !transitioned {
		t.Fatalf("retry dispatch = (transitioned %v, error %v)", transitioned, err)
	}
}

func TestGatewayInvocationStoreLoadRejectsMutatedPlan(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node_invocations.json")
	plan := gatewayTestPlan(t, "inv_mutated", "idem_mutated", time.Now())
	record := GatewayInvocationRecord{
		Target:           "vpn",
		ToolCallID:       "call-1",
		Plan:             plan,
		Descriptor:       gatewayTestDescriptor(),
		ExpectedPlanHash: plan.PlanHash,
		State:            GatewayInvocationPrepared,
		CreatedAt:        time.Now().UnixNano(),
		UpdatedAt:        time.Now().UnixNano(),
	}
	record.Plan.Input = json.RawMessage(`{"argv":["different"]}`)
	data, err := json.Marshal(gatewayInvocationDocument{
		Version: gatewayInvocationStoreVersion,
		Records: map[string]GatewayInvocationRecord{plan.InvocationID: record},
	})
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := os.WriteFile(path, data, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	if _, loadErr := NewGatewayInvocationStore(path, 8, 1024*1024); loadErr == nil {
		t.Fatal("mutated persisted plan was accepted")
	}
}

func TestGatewayInvocationStoreLoadPrunesExpiredPreparedAuthority(t *testing.T) {
	path := filepath.Join(t.TempDir(), "node_invocations.json")
	preparedAt := time.Now().Add(-2 * time.Minute)
	plan := gatewayTestPlan(t, "inv_stale", "idem_stale", preparedAt)
	now := time.Now().UnixNano()
	data, err := json.Marshal(gatewayInvocationDocument{
		Version: gatewayInvocationStoreVersion,
		Records: map[string]GatewayInvocationRecord{plan.InvocationID: {
			Target:           "vpn",
			ToolCallID:       "call-1",
			Plan:             plan,
			Descriptor:       gatewayTestDescriptor(),
			ExpectedPlanHash: plan.PlanHash,
			State:            GatewayInvocationPrepared,
			CreatedAt:        now,
			UpdatedAt:        now,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if writeErr := os.WriteFile(path, data, 0o600); writeErr != nil {
		t.Fatal(writeErr)
	}
	store, err := NewGatewayInvocationStore(path, 8, 1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	_, found, lookupErr := store.ByToolCall(gatewayTestPrincipal(plan), "call-1")
	if lookupErr != nil || found {
		t.Fatalf("expired loaded record = (%v, %v)", found, lookupErr)
	}
	var document gatewayInvocationDocument
	persisted, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if decodeErr := json.Unmarshal(persisted, &document); decodeErr != nil {
		t.Fatal(decodeErr)
	}
	if len(document.Records) != 0 {
		t.Fatalf("persisted stale records = %#v", document.Records)
	}
}

func gatewayTestPlan(
	t *testing.T,
	invocationID string,
	idempotencyKey string,
	preparedAt time.Time,
) ExecutionPlan {
	t.Helper()
	request := invocationRequest(json.RawMessage(`{"argv":["git","status"]}`))
	request.InvocationID = invocationID
	request.IdempotencyKey = idempotencyKey
	plan, err := PrepareExecutionPlan(
		request,
		invocationDescriptor(RiskWrite),
		"local",
		"policy-1",
		preparedAt,
		time.Minute,
	)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func gatewayTestDescriptor() CommandDescriptor {
	return invocationDescriptor(RiskWrite)
}

func gatewayTestPrincipal(plan ExecutionPlan) GatewayInvocationPrincipal {
	return GatewayInvocationPrincipal{
		AgentID:   plan.AgentID,
		SessionID: plan.SessionID,
		ActorID:   plan.ActorID,
	}
}

func gatewayTestOwner(
	target string,
	toolCallID string,
	plan ExecutionPlan,
) GatewayInvocationOwner {
	return GatewayInvocationOwner{
		Target:     target,
		AgentID:    plan.AgentID,
		SessionID:  plan.SessionID,
		ActorID:    plan.ActorID,
		ToolCallID: toolCallID,
	}
}
