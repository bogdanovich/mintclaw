//go:build linux || darwin

package worker

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const buildPinningHelperEnv = "MINTCLAW_WORKER_BUILD_PINNING_HELPER"

func TestCurrentExecutableBuildIDPinsRunningImage(t *testing.T) {
	if os.Getenv(buildPinningHelperEnv) == "1" {
		runBuildPinningHelper(t)
		return
	}
	sourcePath, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	executablePath := filepath.Join(t.TempDir(), "worker-build-helper")
	if err := copyExecutable(sourcePath, executablePath); err != nil {
		t.Fatal(err)
	}
	want, err := ExecutableBuildID(executablePath)
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executablePath, "-test.run=^TestCurrentExecutableBuildIDPinsRunningImage$")
	command.Env = append(os.Environ(), buildPinningHelperEnv+"=1")
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if command.Process != nil {
			_ = command.Process.Kill()
		}
	}()
	reader := bufio.NewReader(stdout)
	ready, err := reader.ReadString('\n')
	if err != nil || strings.TrimSpace(ready) != "ready" {
		t.Fatalf("helper readiness = %q, %v; stderr=%q", ready, err, stderr.String())
	}
	replacementPath := executablePath + ".replacement"
	if err := os.WriteFile(replacementPath, []byte("replacement executable bytes\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(replacementPath, executablePath); err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(stdin, "hash"); err != nil {
		t.Fatal(err)
	}
	got, err := reader.ReadString('\n')
	if err != nil {
		t.Fatalf("read helper build identity: %v; stderr=%q", err, stderr.String())
	}
	if err := command.Wait(); err != nil {
		t.Fatalf("helper exit: %v; stderr=%q", err, stderr.String())
	}
	command.Process = nil
	if strings.TrimSpace(got) != want {
		t.Fatalf("running image build identity = %q, want %q", strings.TrimSpace(got), want)
	}
	replacement, err := ExecutableBuildID(executablePath)
	if err != nil {
		t.Fatal(err)
	}
	if replacement == want {
		t.Fatal("replacement pathname unexpectedly retained the running image identity")
	}
}

func runBuildPinningHelper(t *testing.T) {
	if _, err := fmt.Fprintln(os.Stdout, "ready"); err != nil {
		t.Fatal(err)
	}
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		t.Fatal(err)
	}
	buildID, err := CurrentExecutableBuildID()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(os.Stdout, buildID); err != nil {
		t.Fatal(err)
	}
}

func copyExecutable(sourcePath, targetPath string) error {
	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	target, err := os.OpenFile(targetPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o700)
	if err != nil {
		return errors.Join(err, source.Close())
	}
	_, copyErr := io.Copy(target, source)
	return errors.Join(copyErr, target.Close(), source.Close())
}
