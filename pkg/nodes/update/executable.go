package update

import (
	"debug/elf"
	"debug/macho"
	"errors"
	"io"
)

// ValidateExecutable applies the admitted platform and architecture checks to
// a companion payload without depending on the host operating system.
func ValidateExecutable(file io.ReaderAt, platform string, architecture string) error {
	switch platform {
	case "linux":
		binary, err := elf.NewFile(file)
		if err != nil {
			return errors.New("candidate is not a valid ELF executable")
		}
		defer func() { _ = binary.Close() }()
		expected := elf.EM_X86_64
		if architecture == "arm64" {
			expected = elf.EM_AARCH64
		} else if architecture != "amd64" {
			return errors.New("unsupported Linux companion architecture")
		}
		if binary.Machine != expected || (binary.Type != elf.ET_EXEC && binary.Type != elf.ET_DYN) {
			return errors.New("ELF executable does not match the admitted platform tuple")
		}
	case "darwin":
		binary, err := macho.NewFile(file)
		if err != nil {
			return errors.New("candidate is not a valid Mach-O executable")
		}
		defer func() { _ = binary.Close() }()
		expected := macho.CpuAmd64
		if architecture == "arm64" {
			expected = macho.CpuArm64
		} else if architecture != "amd64" {
			return errors.New("unsupported macOS companion architecture")
		}
		if binary.Cpu != expected || binary.Type != macho.TypeExec {
			return errors.New("Mach-O executable does not match the admitted platform tuple")
		}
	default:
		return errors.New("unsupported companion executable platform")
	}
	return nil
}
