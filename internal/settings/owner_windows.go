//go:build windows

package settings

import "os"

// preserveOwner is a no-op on Windows: the file inherits the profile
// directory's ACL, which is the file's owner by construction in a normal
// deployment (TW_HARNESS_CONFIG to another account is a read-only scenario
// there, not a chown one).
func preserveOwner(string, os.FileInfo) {}
