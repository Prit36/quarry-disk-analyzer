//go:build !windows

package platform

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

var errUnsupported = errors.New("only supported on Windows")

func ShowInExplorer(paths []string) error                { return errUnsupported }
func ShowProperties(owner uintptr, paths []string) error { return errUnsupported }
func DeleteItems(owner uintptr, paths []string, recycle bool) (bool, error) {
	if recycle {
		return false, errUnsupported
	}
	for _, p := range paths {
		if err := os.RemoveAll(p); err != nil {
			return false, err
		}
	}
	return false, nil
}
func ValidateName(name string) error {
	if strings.TrimSpace(name) == "" || strings.ContainsAny(name, `/`) {
		return errors.New("invalid name")
	}
	return nil
}
func RenameItem(path, newName string) (string, error) {
	if err := ValidateName(newName); err != nil {
		return "", err
	}
	target := filepath.Join(filepath.Dir(path), newName)
	if _, err := os.Lstat(target); err == nil {
		return "", errors.New(newName + " already exists")
	}
	return target, os.Rename(path, target)
}
func FileTypeName(ext string) string { return "" }
func ExplorerVerbInstalled() bool    { return false }
func InstallExplorerVerb() error     { return errUnsupported }
func RemoveExplorerVerb() error      { return errUnsupported }
func AttachParentConsole() bool      { return true }
