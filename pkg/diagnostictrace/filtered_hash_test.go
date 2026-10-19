package diagnostictrace

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
)

func TestFilteredHashPreservesNativeBoundary(t *testing.T) {
	filter := strings.NewReplacer("synthetic-secret", "[REDACTED]").Replace
	if FilteredHash("", filter) != "" || FilteredHash("session:synthetic-secret", filter) !=
		fmt.Sprintf("%x", sha256.Sum256([]byte("session:[REDACTED]"))) ||
		FilteredHash("session", nil) != fmt.Sprintf("%x", sha256.Sum256([]byte("session"))) {
		t.Fatal("native hash boundary changed")
	}
}
