package runtimeimage

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"testing"
)

func syntheticFiles() map[string][]byte {
	return map[string][]byte{
		NodePath:              []byte("\x7fELFsynthetic-node"),
		ChromiumPath:          []byte("\x7fELFsynthetic-browser"),
		WorkerPath:            []byte("synthetic worker source"),
		PlaywrightPackagePath: []byte(`{"version":"1.63.0"}`),
	}
}

func syntheticManifest(files map[string][]byte) []byte {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	var buffer strings.Builder
	buffer.WriteString(manifestHeader + "\n")
	for _, name := range names {
		mode := "0444"
		if name == NodePath || name == ChromiumPath {
			mode = "0555"
		}
		fmt.Fprintf(&buffer, "%x %s %d %s\n", sha256.Sum256(files[name]), mode, len(files[name]), name)
	}
	return []byte(buffer.String())
}

func TestManifestExactInventoryAndBounds(t *testing.T) {
	data := string(syntheticManifest(syntheticFiles()))
	entries, err := decodeManifest([]byte(data))
	if err != nil || len(entries) != 4 {
		t.Fatal("synthetic inventory denied")
	}
	lines := strings.Split(data, "\n")
	for _, denied := range []string{
		"", "{}", data + "\n", strings.TrimSuffix(data, "\n"), strings.Repeat("a", maxManifestBytes+1),
		strings.Replace(data, manifestHeader, "mintclaw-credential-browser-runtime-v2", 1),
		strings.Replace(data, " 0555 ", " 0755 ", 1),
		strings.Replace(data, " 0555 ", " 0555  ", 1),
		strings.Replace(data, "bin/node", "../node", 1),
		strings.Replace(data, "bin/node", "/bin/node", 1),
		strings.Replace(data, "bin/node", "bin/../node", 1),
		strings.Replace(data, "bin/node", "bin\\node", 1),
		strings.Replace(data, "bin/node", manifestName, 1),
		strings.Replace(data, lines[1], strings.ToUpper(lines[1]), 1),
		strings.Replace(data, lines[1], lines[1]+"\n"+lines[1], 1),
		strings.Replace(data, lines[1], strings.Replace(lines[1], " 0555 ", " 0444 ", 1), 1),
		strings.Replace(data, " 0555 21 ", " 0555 -1 ", 1),
		strings.Replace(data, " 0555 21 ", " 0555 021 ", 1),
		strings.Replace(data, " 0555 21 ", " 0555 536870913 ", 1),
		strings.Replace(data, "bin/chromium", "bin/other", 1),
	} {
		if _, err = decodeManifest([]byte(denied)); !errors.Is(err, errUnavailable) {
			t.Fatal("ambiguous or expanded inventory admitted")
		}
	}
}

func TestRuntimeUnavailableStartup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, denied := range []context.Context{nil, ctx} {
		if _, err := Open(denied); !errors.Is(err, errUnavailable) {
			t.Fatal("canceled startup admitted")
		}
	}
	if runtime.GOOS != "linux" {
		if _, err := Open(context.Background()); !errors.Is(err, errUnavailable) {
			t.Fatal("unsupported host admitted")
		}
	}
	var image *Image
	if _, err := image.BorrowRoot(context.Background()); !errors.Is(err, errUnavailable) {
		t.Fatal("missing image admitted")
	}
	if image.Close() != nil {
		t.Fatal("empty image close failed")
	}
}
