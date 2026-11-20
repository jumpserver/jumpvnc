package main

import (
	"strings"
	"testing"
)

func TestClipboardTextValidation(t *testing.T) {
	for _, text := range []string{"", "中文\n😀", strings.Repeat("x", clipboardTextLimit)} {
		if err := validClipboardText(text); err != nil {
			t.Fatal(err)
		}
	}
	for _, text := range []string{"a\x00b", string([]byte{0xff}), strings.Repeat("x", clipboardTextLimit+1)} {
		if err := validClipboardText(text); err == nil {
			t.Fatal("accepted invalid clipboard text")
		}
	}
}
