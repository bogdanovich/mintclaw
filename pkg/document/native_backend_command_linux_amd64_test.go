//go:build linux && amd64

package document

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifiedPopplerCommandBindsTheAdmittedExecutableIdentity(t *testing.T) {
	root := t.TempDir()
	candidate := filepath.Join(root, "candidate")
	if err := os.WriteFile(candidate, []byte("#!/bin/sh\nprintf original"), 0o700); err != nil {
		t.Fatal(err)
	}
	if command, executable, err := newVerifiedPopplerCommand(
		candidate,
		strings.Repeat("0", 64),
	); err == nil || command != nil || executable != nil {
		t.Fatalf("unadmitted executable command = %#v, file = %#v, err = %v", command, executable, err)
	}
	digest := executableSHA256(candidate)
	before, err := os.Stat(candidate)
	if err != nil {
		t.Fatal(err)
	}
	command, executable, err := newVerifiedPopplerCommand(candidate, digest)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = executable.Close() }()
	if _, err = executable.WriteAt([]byte("replacement"), 0); err == nil {
		t.Fatal("sealed executable snapshot accepted an in-place write")
	}
	if err = os.WriteFile(candidate, []byte("#!/bin/sh\nprintf replacement"), 0o700); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(candidate)
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(before, after) {
		t.Fatal("test did not mutate the candidate executable in place")
	}
	var output bytes.Buffer
	command.Env = documentBackendEnvironment()
	command.Stdout = &output
	if err = command.Run(); err != nil {
		t.Fatal(err)
	}
	if output.String() != "original" {
		t.Fatalf("verified command executed %q", output.String())
	}
}
