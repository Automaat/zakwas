package config

import (
	"errors"
	"fmt"
	"strings"

	"filippo.io/age"
)

// Secrets are private keys zakwas restores from a password manager. The
// config holds only where they go and how to recognize them.
type Secrets struct {
	Age []AgeFile `yaml:"age" jsonschema_description:"Files of age private keys, such as the identities sops reads. Apply adds the keys a file is missing from Bitwarden; plan and check compare public keys and never open the vault."`
	GPG []GPGKey  `yaml:"gpg" jsonschema_description:"GPG secret keys kept as an exported key in a Bitwarden secure note. Apply imports a note whose key the keyring lacks; plan and check list the keyring and never open the vault."`
}

// Any reports whether there is a secret to restore.
func (s Secrets) Any() bool { return len(s.Age) > 0 || len(s.GPG) > 0 }

// GPGKey names one GPG secret key by where its export is kept and the
// fingerprint that identifies it in the keyring.
type GPGKey struct {
	Bitwarden   string `yaml:"bitwarden" jsonschema:"required,minLength=1" jsonschema_description:"Name of the Bitwarden secure note holding the exported secret key (gpg --export-secret-keys --armor). The whole note is imported."`
	Fingerprint string `yaml:"fingerprint" jsonschema:"required,minLength=1" jsonschema_description:"The 40 hex characters of the fingerprint of the primary key or of a subkey the import must provide, without spaces. A key that lives on a smartcard doesn't count."`
}

// AgeFile is one identity file and the keys it must hold.
type AgeFile struct {
	Dst  string   `yaml:"dst" jsonschema:"required,minLength=1" jsonschema_description:"Identity file: absolute or starting with '~/'. It is kept owner-only (0600). Keys and comments already in it stay."`
	Keys []AgeKey `yaml:"keys" jsonschema:"required,minItems=1" jsonschema_description:"The keys the file must hold."`
}

// AgeKey names one private key by where it is kept and its public half.
type AgeKey struct {
	Bitwarden string `yaml:"bitwarden" jsonschema:"required,minLength=1" jsonschema_description:"Name of the Bitwarden secure note holding the private key."`
	PublicKey string `yaml:"publicKey" jsonschema:"required,minLength=1" jsonschema_description:"The key's public half (age1...), which identifies it without opening the vault."`
}

func (c *Config) validateSecrets(paths Paths, claimDst func(dst, what string)) []error {
	var errs []error
	for i, f := range c.Secrets.Age {
		what := c.where("secrets.age", i)
		switch {
		case f.Dst == "":
			errs = append(errs, fmt.Errorf("%s: dst is required", what))
		case holdsPrivateKey(f.Dst):
			errs = append(errs, fmt.Errorf("%s: dst %s", what, privateKeyAdvice))
		default:
			claimDst(f.Dst, what)
			for _, root := range c.Paths(paths.Home).Roots() {
				if PathsOverlap(paths.Dst(f.Dst), root) {
					errs = append(errs, fmt.Errorf("%s: %q is inside a config repo, where a private key could get committed", what, f.Dst))
				}
			}
		}
		if len(f.Keys) == 0 {
			errs = append(errs, fmt.Errorf("%s: keys is required", what))
		}
		seen := map[string]bool{}
		for j, k := range f.Keys {
			key := fmt.Sprintf("%s.keys[%d]", what, j)
			switch {
			case k.Bitwarden == "":
				errs = append(errs, fmt.Errorf("%s: bitwarden is required", key))
			case holdsPrivateKey(k.Bitwarden):
				errs = append(errs, fmt.Errorf("%s: bitwarden %s", key, privateKeyAdvice))
			}
			if err := checkPublicKey(k.PublicKey); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", key, err))
			} else if seen[k.PublicKey] {
				errs = append(errs, fmt.Errorf("%s: publicKey %s is listed twice", key, k.PublicKey))
			}
			seen[k.PublicKey] = true
		}
	}
	seen := map[string]bool{}
	for i, k := range c.Secrets.GPG {
		what := c.where("secrets.gpg", i)
		switch {
		case k.Bitwarden == "":
			errs = append(errs, fmt.Errorf("%s: bitwarden is required", what))
		case holdsPrivateKey(k.Bitwarden):
			errs = append(errs, fmt.Errorf("%s: bitwarden %s", what, privateKeyAdvice))
		}
		if err := checkFingerprint(k.Fingerprint); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", what, err))
			continue
		}
		fpr := strings.ToUpper(k.Fingerprint)
		if seen[fpr] {
			errs = append(errs, fmt.Errorf("%s: fingerprint %s is listed twice", what, fpr))
		}
		seen[fpr] = true
	}
	return errs
}

// checkFingerprint never quotes a value it rejects: a pasted private key
// would otherwise land in the error message.
func checkFingerprint(fpr string) error {
	switch {
	case fpr == "":
		return errors.New("fingerprint is required")
	case holdsPrivateKey(fpr):
		return errors.New("fingerprint " + privateKeyAdvice)
	}
	if len(fpr) != 40 {
		return errors.New("fingerprint must be 40 hex characters, without spaces")
	}
	for _, r := range fpr {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return errors.New("fingerprint must be 40 hex characters, without spaces")
		}
	}
	return nil
}

const privateKeyAdvice = "holds a private key; remove it from the config and rotate it if it was committed"

// holdsPrivateKey catches a key pasted into the wrong field, so it is refused
// without being quoted, and never reaches a plan or the apply history.
func holdsPrivateKey(value string) bool {
	upper := strings.ToUpper(value)
	return strings.Contains(upper, "AGE-SECRET-KEY-") || strings.Contains(upper, "PGP PRIVATE KEY BLOCK")
}

// checkPublicKey never quotes a value it rejects: a pasted private key would
// otherwise land in the error message.
func checkPublicKey(key string) error {
	switch {
	case key == "":
		return errors.New("publicKey is required")
	case holdsPrivateKey(key):
		return errors.New("publicKey " + privateKeyAdvice)
	}
	if _, err := age.ParseX25519Recipient(key); err != nil {
		return errors.New("publicKey isn't an age X25519 public key (age1...)")
	}
	return nil
}
