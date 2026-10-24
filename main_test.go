package main

import "testing"

func TestExplorerHelperNeedsPaths(t *testing.T) {
	if code := revealInExplorer(nil); code != 2 {
		t.Fatalf("empty helper invocation exited %d", code)
	}
}
