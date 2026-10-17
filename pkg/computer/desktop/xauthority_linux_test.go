//go:build linux

package desktop

import (
	"bytes"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/bogdanovich/mintclaw/pkg/computer"
)

func authorityRecord(family uint16, host, display, protocol string, cookie []byte) []byte {
	var output bytes.Buffer
	_ = binary.Write(&output, binary.BigEndian, family)
	for _, field := range [][]byte{[]byte(host), []byte(display), []byte(protocol), cookie} {
		_ = binary.Write(&output, binary.BigEndian, uint16(len(field)))
		_, _ = output.Write(field)
	}
	return output.Bytes()
}

func TestX11ExplicitLocalDisplay(t *testing.T) {
	for _, value := range []string{"", "host:1", "localhost:10.0", "tcp/host:0", "/tmp/X1", ":1\n", ":65536", ":1.256"} {
		if _, err := x11Display(value); !errors.Is(err, computer.ErrInvalid) {
			t.Fatal("nonlocal/invalid display accepted", err)
		}
	}
	endpoint, err := x11Display(":001.2")
	if err != nil || endpoint.socket != "/tmp/.X11-unix/X1" || endpoint.number != "1" || endpoint.screen != 2 {
		t.Fatal("display coordinates not retained", err)
	}
}

func TestX11AuthorityIsExplicitBoundedAndPrivate(t *testing.T) {
	cookie := []byte("synthetic-cookie")
	valid := authorityRecord(256, "fixture", "1", "MIT-MAGIC-COOKIE-1", cookie)
	if got, err := parseX11Cookie(valid, "fixture", "1"); err != nil || len(got) != 32 {
		t.Fatal("valid explicit authority refused", err)
	}
	for _, record := range [][]byte{
		valid[:len(valid)-1], authorityRecord(256, "other", "1", "MIT-MAGIC-COOKIE-1", cookie),
		authorityRecord(256, "fixture", "2", "MIT-MAGIC-COOKIE-1", cookie),
		authorityRecord(256, "fixture", "1", "other", cookie),
		authorityRecord(256, "fixture", "1", "MIT-MAGIC-COOKIE-1", []byte("short")),
	} {
		if _, err := parseX11Cookie(record, "fixture", "1"); !errors.Is(err, computer.ErrDenied) {
			t.Fatal("wrong authority accepted", err)
		}
	}
	// Empty display/wild family is the standard Xauthority wildcard, not an
	// instruction to choose a different endpoint or try unauthenticated access.
	if _, err := parseX11Cookie(
		authorityRecord(65535, "", "", "MIT-MAGIC-COOKIE-1", cookie),
		"fixture",
		"1",
	); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "authority")
	host, err := os.Hostname()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, authorityRecord(256, host, "1", "MIT-MAGIC-COOKIE-1", cookie), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := x11Cookie(path, "1"); !errors.Is(err, computer.ErrDenied) {
		t.Fatal("public authority file accepted", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		t.Fatal(err)
	}
	if got, err := x11Cookie(path, "1"); err != nil || len(got) != 32 {
		t.Fatal("private explicit authority refused", err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := x11Cookie(link, "1"); !errors.Is(err, computer.ErrDenied) {
		t.Fatal("authority symlink followed", err)
	}
}
