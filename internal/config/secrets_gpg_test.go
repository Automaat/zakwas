package config

import (
	"path/filepath"
	"strings"
	"testing"
)

const testFingerprint = "4C87E4377EC834C405AE619989B58A1317507C65"

func TestGPGSecretsValidation(t *testing.T) {
	const block = "-----BEGIN PGP PRIVATE KEY BLOCK-----"
	valid := "{bitwarden: note, fingerprint: " + testFingerprint + "}"
	for _, test := range []struct {
		name, yaml, want string
	}{
		{"valid", "secrets: {gpg: [" + valid + "]}", ""},
		{"lower case", "secrets: {gpg: [{bitwarden: note, fingerprint: " + strings.ToLower(testFingerprint) + "}]}", ""},
		{"next to age", "secrets: {age: [{dst: ~/k.txt, keys: [{bitwarden: note, publicKey: " + testPublicKey + "}]}], gpg: [" + valid + "]}", ""},
		{"no note", "secrets: {gpg: [{fingerprint: " + testFingerprint + "}]}", "bitwarden is required"},
		{"no fingerprint", "secrets: {gpg: [{bitwarden: note}]}", "fingerprint is required"},
		{"short fingerprint", "secrets: {gpg: [{bitwarden: note, fingerprint: C25DFDF396ADF455}]}", "40 hex characters"},
		{"spaces", "secrets: {gpg: [{bitwarden: note, fingerprint: \"4C87 E437 7EC8 34C4 05AE 6199 89B5 8A13 1750 7C65\"}]}", "40 hex characters"},
		{"not hex", "secrets: {gpg: [{bitwarden: note, fingerprint: \"ZC87E4377EC834C405AE619989B58A1317507C65\"}]}", "40 hex characters"},
		{"private key as the note", "secrets: {gpg: [{bitwarden: \"" + block + "\", fingerprint: " + testFingerprint + "}]}", "bitwarden holds a private key"},
		{"private key as the fingerprint", "secrets: {gpg: [{bitwarden: note, fingerprint: \"" + block + "\"}]}", "fingerprint holds a private key"},
		{"listed twice", "secrets: {gpg: [" + valid + ", {bitwarden: other, fingerprint: " + strings.ToLower(testFingerprint) + "}]}", "listed twice"},
		{"unknown key", "secrets: {gpg: [{bitwarden: note, fingerprint: " + testFingerprint + ", nope: 1}]}", "unknown field"},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := ParseFor([]byte(test.yaml), filepath.Join(t.TempDir(), FileName), t.TempDir(), "darwin")
			switch {
			case test.want == "" && err != nil:
				t.Fatal(err)
			case test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)):
				t.Fatalf("err = %v, want %q", err, test.want)
			}
			if err != nil && strings.Contains(strings.ToUpper(err.Error()), "PGP PRIVATE KEY BLOCK") {
				t.Error("an error quotes the private key")
			}
		})
	}
}

func TestGPGSecretsAreRefusedOnWindows(t *testing.T) {
	yaml := "secrets: {gpg: [{bitwarden: note, fingerprint: " + testFingerprint + "}]}"
	_, err := ParseFor([]byte(yaml), filepath.Join(t.TempDir(), FileName), t.TempDir(), "windows")
	if err == nil || !strings.Contains(err.Error(), "secrets") {
		t.Fatalf("err = %v", err)
	}
}
