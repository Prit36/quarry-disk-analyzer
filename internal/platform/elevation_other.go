//go:build !windows

package platform

import "fmt"

func Elevated() bool                { return true }
func RelaunchElevated(string) error { return fmt.Errorf("elevation is only supported on Windows") }
