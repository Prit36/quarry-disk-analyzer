package index

import (
	"os"

	"golang.org/x/sys/windows"
)

// Keep alias detection linear even for thousands of identical or empty files.
// The identity comes from the same open handle whose content was hashed.
func readDuplicateIdentity(file *os.File) (duplicateIdentity, bool) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(windows.Handle(file.Fd()), &info); err != nil {
		return duplicateIdentity{}, false // fall back to os.SameFile
	}
	if info.FileIndexHigh == 0 && info.FileIndexLow == 0 {
		return duplicateIdentity{}, false // some providers do not supply an ID
	}
	return duplicateIdentity{info.VolumeSerialNumber, info.FileIndexHigh, info.FileIndexLow}, true
}
