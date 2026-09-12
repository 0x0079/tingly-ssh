package auth

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func write(t *testing.T, body string, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "credentials")
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLookupIdentifiesTheDevice(t *testing.T) {
	laptop, phone := []byte("laptop-secret"), []byte("phone-secret")
	body := "# label  hash  not-after\n" +
		Record("laptop-mbp14", laptop, time.Time{}) + "\n" +
		Record("phone-termux", phone, time.Time{}) + "\n"
	store, err := LoadStore(write(t, body, 0o644))
	if err != nil {
		t.Fatal(err)
	}
	if store.Shared() {
		t.Fatal("a file of records must not be treated as a shared token")
	}
	for token, want := range map[string]string{
		"laptop-secret": "laptop-mbp14",
		"phone-secret":  "phone-termux",
	} {
		got, err := store.Lookup([]byte(token))
		if err != nil {
			t.Fatalf("%s: %v", token, err)
		}
		if got.Label != want {
			t.Fatalf("token %s resolved to %q, want %q", token, got.Label, want)
		}
	}
	if _, err := store.Lookup([]byte("not-a-token")); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("want ErrUnauthorized, got %v", err)
	}
}

func TestExpiredCredentialIsRefusedWithItsLabel(t *testing.T) {
	tok := []byte("expired-secret")
	body := Record("ci-runner-3", tok, time.Now().AddDate(0, 0, -2)) + "\n"
	store, err := LoadStore(write(t, body, 0o644))
	if err != nil {
		t.Fatal(err)
	}
	cred, err := store.Lookup(tok)
	if !errors.Is(err, ErrExpired) {
		t.Fatalf("want ErrExpired, got %v", err)
	}
	// The label comes back even on failure, so the log can say which device.
	if cred.Label != "ci-runner-3" {
		t.Fatalf("label is %q", cred.Label)
	}
	if !strings.Contains(err.Error(), "ci-runner-3") {
		t.Fatalf("error should name the credential: %v", err)
	}
}

func TestNotAfterAcceptsDateAndTimestamp(t *testing.T) {
	day, err := ParseNotAfter("2027-06-01")
	if err != nil {
		t.Fatal(err)
	}
	// A bare date means "valid through that day".
	if day.UTC().Format("2006-01-02 15:04:05") != "2027-06-01 23:59:59" {
		t.Fatalf("date parsed to %v", day.UTC())
	}
	ts, err := ParseNotAfter("2027-06-01T10:00:00Z")
	if err != nil {
		t.Fatal(err)
	}
	if ts.UTC().Hour() != 10 {
		t.Fatalf("timestamp parsed to %v", ts.UTC())
	}
	if _, err := ParseNotAfter("next tuesday"); err == nil {
		t.Fatal("expected a parse error")
	}
}

func TestSharedTokenFileStillWorks(t *testing.T) {
	path := write(t, "legacy-shared-token\n", 0o600)
	store, err := LoadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if !store.Shared() {
		t.Fatal("a bare token file should report Shared")
	}
	cred, err := store.Lookup([]byte("legacy-shared-token"))
	if err != nil {
		t.Fatal(err)
	}
	if cred.Label != SharedLabel {
		t.Fatalf("label is %q, want %q", cred.Label, SharedLabel)
	}
}

func TestSharedTokenFileMustBePrivate(t *testing.T) {
	path := write(t, "legacy-shared-token\n", 0o644)
	if _, err := LoadStore(path); err == nil {
		t.Fatal("a world-readable raw token file must be refused")
	}
	// A file of hashes carries no secret, so loose permissions are fine.
	hashes := write(t, Record("laptop", []byte("x"), time.Time{})+"\n", 0o644)
	if _, err := LoadStore(hashes); err != nil {
		t.Fatalf("hashed records should not require 0600: %v", err)
	}
}

func TestReloadKeepsTheOldSetOnError(t *testing.T) {
	tok := []byte("keep-me")
	path := write(t, Record("laptop", tok, time.Time{})+"\n", 0o644)
	store, err := LoadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("this is not a record\nnor is this\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Reload(); err == nil {
		t.Fatal("expected the broken file to be refused")
	}
	// A typo during an edit must not lock every client out.
	if _, err := store.Lookup(tok); err != nil {
		t.Fatalf("previous credentials should still apply: %v", err)
	}
}

func TestReloadPicksUpRevocation(t *testing.T) {
	a, b := []byte("token-a"), []byte("token-b")
	path := write(t, Record("a", a, time.Time{})+"\n"+Record("b", b, time.Time{})+"\n", 0o644)
	store, err := LoadStore(path)
	if err != nil {
		t.Fatal(err)
	}
	if store.Len() != 2 {
		t.Fatalf("loaded %d credentials", store.Len())
	}
	// Revoking a device is deleting its line.
	if err := os.WriteFile(path, []byte(Record("a", a, time.Time{})+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := store.Reload(); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Lookup(b); !errors.Is(err, ErrUnauthorized) {
		t.Fatalf("revoked credential still works: %v", err)
	}
	if _, err := store.Lookup(a); err != nil {
		t.Fatalf("remaining credential broke: %v", err)
	}
}

func TestMalformedRecordsAreRejected(t *testing.T) {
	cases := map[string]string{
		"bad prefix":      "laptop md5:abcd\n",
		"bad base64":      "laptop sha256:!!!!\n",
		"short hash":      "laptop sha256:AAAA\n",
		"bad not-after":   Record("laptop", []byte("x"), time.Time{}) + " soon\n",
		"token plus rest": "raw-token\nlaptop sha256:AAAA\n",
		"two bare words":  "laptop\nphone\n",
		"empty":           "# only a comment\n",
	}
	for name, body := range cases {
		if _, err := LoadStore(write(t, body, 0o600)); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}
