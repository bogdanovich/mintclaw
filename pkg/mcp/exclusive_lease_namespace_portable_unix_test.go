//go:build !windows && !linux

package mcp

import (
	"errors"
	"fmt"
	"net"
	"path/filepath"
	"slices"
	"testing"
)

func TestExclusiveLeaseNamespaceIdentityIsStableAndBounded(t *testing.T) {
	path := filepath.Join(t.TempDir(), "playwright.lock")
	firstKey, firstPorts := exclusiveLeaseNamespaceIdentity(path)
	secondKey, secondPorts := exclusiveLeaseNamespaceIdentity(path)
	if firstKey != secondKey || !slices.Equal(firstPorts, secondPorts) {
		t.Fatal("namespace identity is not deterministic")
	}
	if len(firstKey) != 64 || len(firstPorts) != exclusiveLeasePortCandidates {
		t.Fatalf("namespace identity = %d-byte key and %d ports", len(firstKey), len(firstPorts))
	}
	if !slices.IsSorted(firstPorts) {
		t.Fatalf("namespace ports are not sorted: %v", firstPorts)
	}
	for index, port := range firstPorts {
		if port < exclusiveLeasePortBase ||
			port >= exclusiveLeasePortBase+exclusiveLeasePortCount {
			t.Fatalf("namespace port %d is outside the admitted range", port)
		}
		if index > 0 && firstPorts[index-1] == port {
			t.Fatalf("namespace ports contain duplicate %d", port)
		}
	}
}

func TestExclusiveLeaseNamespaceFindsSameIdentityAfterFreeCandidate(t *testing.T) {
	path, key, ports := availableNamespaceTestIdentity(t)
	listener, err := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", ports[1]))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go serveExclusiveLeaseNamespace(listener, key, done)

	namespace, err := acquireExclusiveLeaseNamespace(path)
	if namespace != nil {
		_ = namespace.close()
		t.Fatal("namespace contender acquired an earlier free candidate")
	}
	if !errors.Is(err, errExclusiveLeaseBusy) {
		t.Fatalf("namespace contender error = %v, want busy", err)
	}
	if err = listener.Close(); err != nil {
		t.Fatal(err)
	}
	<-done
	reacquired, err := acquireExclusiveLeaseNamespace(path)
	if err != nil {
		t.Fatalf("namespace reacquire error = %v", err)
	}
	if err = reacquired.close(); err != nil {
		t.Fatalf("namespace release error = %v", err)
	}
}

func availableNamespaceTestIdentity(t *testing.T) (string, string, []int) {
	t.Helper()
	root := t.TempDir()
	for attempt := 0; attempt < 1000; attempt++ {
		path := filepath.Join(root, fmt.Sprintf("playwright-%d.lock", attempt))
		key, ports := exclusiveLeaseNamespaceIdentity(path)
		first, firstErr := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", ports[0]))
		if firstErr != nil {
			continue
		}
		second, secondErr := net.Listen("tcp4", fmt.Sprintf("127.0.0.1:%d", ports[1]))
		_ = first.Close()
		if secondErr != nil {
			continue
		}
		_ = second.Close()
		return path, key, ports
	}
	t.Fatal("could not find two available namespace test candidates")
	return "", "", nil
}
