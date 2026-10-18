//go:build !windows

package index

func rawAlloc(n int) []byte { return make([]byte, n) }

func rawFree([]byte) {}
