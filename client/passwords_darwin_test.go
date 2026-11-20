package main

import (
	"crypto/rand"
	"errors"
	"os"
	"testing"
)

func TestKeychainCredentialRoundTrip(t *testing.T) {
	if os.Getenv("SVNC_TEST_KEYCHAIN") != "1" {
		t.Skip("requires an unlocked local login keychain")
	}
	store := newPasswordStore()
	ref := rand.Text()
	t.Cleanup(func() {
		if err := store.Delete(ref); err != nil {
			t.Error(err)
		}
	})
	expected := savedPasswords{VNC: "fixture 'quoted' \"double\"\n中文", SSH: "fixture with spaces & symbols $"}
	if err := store.Set(ref, expected); err != nil {
		t.Fatal(err)
	}
	actual, err := store.Get(ref)
	if err != nil || actual != expected {
		t.Fatalf("keychain round trip failed: %v", err)
	}
	if err := store.Delete(ref); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(ref); !errors.Is(err, errPasswordNotFound) {
		t.Fatal("keychain item was not deleted")
	}
}
