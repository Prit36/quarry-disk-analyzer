//go:build !windows

package index

import "os"

func readDuplicateIdentity(file *os.File) (duplicateIdentity, bool) {
	return duplicateIdentity{}, false // portable os.SameFile fallback
}
