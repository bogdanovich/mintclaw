package media

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
)

func pinMediaContent(path string) (ContentIdentity, error) {
	file, info, err := openMediaSourceNoFollow(path)
	if err != nil {
		return ContentIdentity{}, err
	}
	defer func() { _ = file.Close() }()
	return contentIdentityForOpenFile(file, path, info)
}

func contentIdentityForOpenFile(file *os.File, path string, initial os.FileInfo) (ContentIdentity, error) {
	if file == nil || initial == nil || !initial.Mode().IsRegular() {
		return ContentIdentity{}, errors.New("media source is not a regular file")
	}
	first, err := hashOpenMediaFile(file)
	if err != nil {
		return ContentIdentity{}, err
	}
	second, err := hashOpenMediaFile(file)
	if err != nil {
		return ContentIdentity{}, err
	}
	final, err := file.Stat()
	if err != nil {
		return ContentIdentity{}, err
	}
	current, err := os.Lstat(path)
	if err != nil {
		return ContentIdentity{}, err
	}
	if first != second || initial.Size() != final.Size() ||
		!initial.ModTime().Equal(final.ModTime()) || current.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(initial, final) || !os.SameFile(final, current) {
		return ContentIdentity{}, errors.New("media source changed while pinning")
	}
	return first, nil
}

func hashOpenMediaFile(file *os.File) (ContentIdentity, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return ContentIdentity{}, err
	}
	hash := sha256.New()
	size, err := io.Copy(hash, file)
	if err != nil {
		return ContentIdentity{}, err
	}
	return ContentIdentity{Size: size, SHA256: hex.EncodeToString(hash.Sum(nil))}, nil
}
