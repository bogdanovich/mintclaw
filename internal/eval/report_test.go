package eval

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
)

func TestPrivateReportRefusesUnsafeLocationAndOverwrite(t *testing.T) {
	fixture := newAdmissionFixture(t)
	for _, parent := range []string{fixture.workspace, fixture.home, t.TempDir(), filepath.Join(fixture.home, "absent")} {
		if _, err := newPrivateReport(
			parent,
			fixture.home,
			fixture.workspace,
			uuid.NewString(),
		); !errors.Is(
			err,
			ErrReport,
		) {
			t.Fatal("unsafe report location admitted")
		}
	}
	id := uuid.NewString()
	report, err := newPrivateReport(fixture.output, fixture.home, fixture.workspace, id)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = report.root.Close() }()
	if _, err = newPrivateReport(fixture.output, fixture.home, fixture.workspace, id); !errors.Is(err, ErrReport) {
		t.Fatal("existing report directory reused")
	}
	if err = report.write("evidence.json", map[string]string{"value": "first"}); err != nil {
		t.Fatal(err)
	}
	if !errors.Is(report.write("evidence.json", map[string]string{"value": "second"}), ErrReport) ||
		!errors.Is(report.writeBytes("../../outside", []byte("secret")), ErrReport) {
		t.Fatal("report overwrite or traversal admitted")
	}
	if _, err = os.Stat(filepath.Join(fixture.home, "outside")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("report escaped its owned directory")
	}
}

func TestPrivateReportRefusesSymlinkEscapeAndPublicPermissions(t *testing.T) {
	fixture := newAdmissionFixture(t)
	alias := filepath.Join(fixture.home, "outside-reports")
	outside := t.TempDir()
	if err := os.Symlink(outside, alias); err != nil {
		t.Fatal(err)
	}
	if _, err := newPrivateReport(
		alias,
		fixture.home,
		fixture.workspace,
		uuid.NewString(),
	); !errors.Is(
		err,
		ErrReport,
	) {
		t.Fatal("outside symlink admitted")
	}
	if err := os.Chmod(fixture.output, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := newPrivateReport(
		fixture.output,
		fixture.home,
		fixture.workspace,
		uuid.NewString(),
	); !errors.Is(
		err,
		ErrReport,
	) {
		t.Fatal("public report parent admitted")
	}
}
