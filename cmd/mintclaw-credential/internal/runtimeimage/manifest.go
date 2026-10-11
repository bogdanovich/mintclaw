package runtimeimage

import (
	"encoding/hex"
	"errors"
	"path"
	"strconv"
	"strings"
)

const (
	manifestName          = "runtime.manifest"
	manifestHeader        = "mintclaw-credential-browser-runtime-v1"
	maxManifestBytes      = 2 << 20
	maxFiles              = 8192
	maxFileBytes          = 512 << 20
	maxRuntimeBytes       = 2 << 30
	NodePath              = "bin/node"
	ChromiumPath          = "bin/chromium"
	WorkerPath            = "runtime/browser/credential-login/worker.cjs"
	PlaywrightPackagePath = "runtime/browser/playwright-library/node_modules/playwright/package.json"
)

var errUnavailable = errors.New("credential browser runtime unavailable")

type entry struct {
	digest [32]byte
	mode   uint32
	size   int64
}

// The operator-generated manifest is itself protected by the runtime mount.
// Its exact sorted inventory excludes only the manifest; hashes are integrity
// checks, not proof that the operator selected authentic vendor artifacts.
func decodeManifest(data []byte) (map[string]entry, error) {
	if len(data) == 0 || len(data) > maxManifestBytes || data[len(data)-1] != '\n' {
		return nil, errUnavailable
	}
	lines := strings.Split(string(data[:len(data)-1]), "\n")
	if len(lines) < 5 || len(lines) > maxFiles+1 || lines[0] != manifestHeader {
		return nil, errUnavailable
	}
	entries := make(map[string]entry, len(lines)-1)
	var total int64
	previous := ""
	for _, line := range lines[1:] {
		fields := strings.Split(line, " ")
		if len(fields) != 4 || len(fields[0]) != 64 || strings.ToLower(fields[0]) != fields[0] ||
			(fields[1] != "0444" && fields[1] != "0555") || !validRelativePath(fields[3]) ||
			fields[3] == manifestName || fields[3] <= previous {
			return nil, errUnavailable
		}
		digest, err := hex.DecodeString(fields[0])
		size, sizeErr := strconv.ParseInt(fields[2], 10, 64)
		if err != nil || len(digest) != 32 || sizeErr != nil || size < 0 || size > maxFileBytes ||
			strconv.FormatInt(size, 10) != fields[2] || total > maxRuntimeBytes-size {
			return nil, errUnavailable
		}
		value := entry{mode: 0o444, size: size}
		if fields[1] == "0555" {
			value.mode = 0o555
		}
		copy(value.digest[:], digest)
		entries[fields[3]] = value
		total += size
		previous = fields[3]
	}
	for _, name := range []string{NodePath, ChromiumPath, WorkerPath, PlaywrightPackagePath} {
		value, found := entries[name]
		if !found || value.size == 0 || ((name == NodePath || name == ChromiumPath) && value.mode != 0o555) {
			return nil, errUnavailable
		}
	}
	return entries, nil
}

func validRelativePath(value string) bool {
	if value == "" || len(value) > 512 || strings.HasPrefix(value, "/") || path.Clean(value) != value ||
		value == "." || value == ".." || strings.HasPrefix(value, "../") || strings.Count(value, "/") > 24 {
		return false
	}
	for _, character := range []byte(value) {
		if character < 0x21 || character > 0x7e || character == '\\' {
			return false
		}
	}
	return true
}
