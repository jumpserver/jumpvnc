package main

import (
	"os"
	"runtime"
	"testing"
)

func setTestHome(t *testing.T, home string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
}

func assertPrivateFile(t *testing.T, filename string) {
	t.Helper()
	info, err := os.Stat(filename)
	if err != nil {
		t.Fatal(err)
	}
	// Windows does not expose Unix permission bits through os.FileMode.
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("private file permissions: %v", info.Mode())
	}
}
