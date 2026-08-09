package main

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"os"

	nodeupdate "github.com/bogdanovich/mintclaw/pkg/nodes/update"
)

func validateReleaseArchive(path string, platform string, architecture string) error {
	archive, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open archive: %w", err)
	}
	defer func() { _ = archive.Close() }()

	compressed, err := gzip.NewReader(archive)
	if err != nil {
		return errors.New("archive is not gzip")
	}
	defer func() { _ = compressed.Close() }()
	compressed.Multistream(false)

	reader := tar.NewReader(compressed)
	header, err := reader.Next()
	if err != nil || header.Name != "mintclaw-node" ||
		header.Typeflag != tar.TypeReg || header.Linkname != "" ||
		header.Size <= 0 || header.Size > nodeupdate.MaxNodeArtifactBytes ||
		header.Mode&0o7000 != 0 || header.Mode&0o111 == 0 {
		return errors.New("archive does not contain one bounded executable")
	}

	candidate, err := os.CreateTemp("", "mintclaw-node-release-candidate-*")
	if err != nil {
		return errors.New("create candidate temporary")
	}
	candidatePath := candidate.Name()
	defer os.Remove(candidatePath)
	defer func() { _ = candidate.Close() }()

	written, err := io.Copy(candidate, io.LimitReader(reader, header.Size+1))
	if err != nil || written != header.Size {
		return errors.New("candidate executable was incomplete")
	}
	if _, err = reader.Next(); !errors.Is(err, io.EOF) {
		return errors.New("archive contains additional entries")
	}
	if err = nodeupdate.ValidateExecutable(candidate, platform, architecture); err != nil {
		return err
	}
	return nil
}
