package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testPublicKey = "age1ql3z7hjy54pw3hyww5ayyfg7zqgvc7w3j2elw8zmrj2kg5sfn9aqmcac8p"

func TestSecretsValidation(t *testing.T) {
	const privateKey = "AGE-SECRET-KEY-1GFPYYSJZGFPYYSJZGFPYYSJZGFPYYSJZGFPYYSJZGFPYYSJZGFPQ4EGAEX"
	valid := "{bitwarden: note, publicKey: " + testPublicKey + "}"
	for _, test := range []struct {
		name, yaml, want string
	}{
		{"valid", "secrets: {age: [{dst: ~/k.txt, keys: [" + valid + "]}]}", ""},
		{"no dst", "secrets: {age: [{keys: [" + valid + "]}]}", "dst is required"},
		{"relative dst", "secrets: {age: [{dst: k.txt, keys: [" + valid + "]}]}", "k.txt"},
		{"no keys", "secrets: {age: [{dst: ~/k.txt}]}", "keys is required"},
		{"no note", "secrets: {age: [{dst: ~/k.txt, keys: [{publicKey: " + testPublicKey + "}]}]}", "bitwarden is required"},
		{"no public key", "secrets: {age: [{dst: ~/k.txt, keys: [{bitwarden: note}]}]}", "publicKey is required"},
		{"bad public key", "secrets: {age: [{dst: ~/k.txt, keys: [{bitwarden: note, publicKey: age1nope}]}]}", "isn't an age"},
		{"private key", "secrets: {age: [{dst: ~/k.txt, keys: [{bitwarden: note, publicKey: " + privateKey + "}]}]}", "holds a private key"},
		{"private key as the note", "secrets: {age: [{dst: ~/k.txt, keys: [{bitwarden: " + privateKey + ", publicKey: " + testPublicKey + "}]}]}", "bitwarden holds a private key"},
		{"private key as the dst", "secrets: {age: [{dst: " + privateKey + ", keys: [" + valid + "]}]}", "dst holds a private key"},
		{"lower case private key as the note", "secrets: {age: [{dst: ~/k.txt, keys: [{bitwarden: " + strings.ToLower(privateKey) + ", publicKey: " + testPublicKey + "}]}]}", "bitwarden holds a private key"},
		{"listed twice", "secrets: {age: [{dst: ~/k.txt, keys: [" + valid + ", " + valid + "]}]}", "listed twice"},
		{"overlaps files", "files: [{src: a, dst: ~/k.txt}]\nsecrets: {age: [{dst: ~/k.txt, keys: [" + valid + "]}]}", "overlaps"},
		{"overlaps itself", "secrets: {age: [{dst: ~/k.txt, keys: [" + valid + "]}, {dst: ~/k.txt, keys: [" + valid + "]}]}", "overlaps"},
		{"unknown key", "secrets: {age: [{dst: ~/k.txt, keys: [{bitwarden: note, publicKey: " + testPublicKey + ", nope: 1}]}]}", "unknown field"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, home := t.TempDir(), t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "a"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := ParseFor([]byte(test.yaml), filepath.Join(root, FileName), home, "darwin")
			switch {
			case test.want == "" && err != nil:
				t.Fatal(err)
			case test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)):
				t.Fatalf("err = %v, want %q", err, test.want)
			}
			if err != nil && strings.Contains(strings.ToUpper(err.Error()), privateKey) {
				t.Error("an error quotes the private key")
			}
		})
	}
}

func TestSecretsRefusesADestinationInTheConfigRepo(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	dst := filepath.Join(root, "keys.txt")
	yaml := "secrets: {age: [{dst: " + dst + ", keys: [{bitwarden: note, publicKey: " + testPublicKey + "}]}]}"
	_, err := ParseFor([]byte(yaml), filepath.Join(root, FileName), home, "darwin")
	if err == nil || !strings.Contains(err.Error(), "inside a config repo") {
		t.Fatalf("err = %v", err)
	}
}

func TestSecretsRefusesASymlinkIntoTheConfigRepo(t *testing.T) {
	root, home := t.TempDir(), t.TempDir()
	if err := os.Symlink(root, filepath.Join(home, "dots")); err != nil {
		t.Skip(err)
	}
	yaml := "secrets: {age: [{dst: ~/dots/keys.txt, keys: [{bitwarden: note, publicKey: " + testPublicKey + "}]}]}"
	_, err := ParseFor([]byte(yaml), filepath.Join(root, FileName), home, "darwin")
	if err == nil || !strings.Contains(err.Error(), "inside a config repo") {
		t.Fatalf("err = %v", err)
	}
}

func TestSecretsAreNotSupportedOnWindows(t *testing.T) {
	yaml := "secrets: {age: [{dst: ~/k.txt, keys: [{bitwarden: note, publicKey: " + testPublicKey + "}]}]}"
	_, err := ParseFor([]byte(yaml), filepath.Join(t.TempDir(), FileName), t.TempDir(), "windows")
	if err == nil || !strings.Contains(err.Error(), "secrets") {
		t.Fatalf("err = %v", err)
	}
}

func TestSecretsRefusesADestinationInALayersRepo(t *testing.T) {
	root, layerRoot, home := t.TempDir(), t.TempDir(), t.TempDir()
	layerPath := filepath.Join(layerRoot, FileName)
	if err := os.WriteFile(layerPath, []byte("files: []\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	dst := filepath.Join(layerRoot, "keys.txt")
	yaml := "secrets: {age: [{dst: " + dst + ", keys: [{bitwarden: note, publicKey: " + testPublicKey + "}]}]}"
	_, err := ParseLayered([]byte(yaml), filepath.Join(root, FileName), home, "darwin", []Layer{{Name: "work", Path: layerPath}})
	if err == nil || !strings.Contains(err.Error(), "inside a config repo") {
		t.Fatalf("err = %v", err)
	}
}
