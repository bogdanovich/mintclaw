//go:build darwin

package approval

// macOS does not expose its ACL through the Linux POSIX ACL xattr interface.
// This trusted-host store checks current ownership and private POSIX modes;
// operator setup must use an unshared directory without additional ACL grants.
func checkOwnerACL(int) error { return nil }
