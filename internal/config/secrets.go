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
		if f.Dst == "" {
			errs = append(errs, fmt.Errorf("%s: dst is required", what))
		}
		claimDst(f.Dst, what)
		for _, root := range paths.Roots() {
			if f.Dst != "" && PathsOverlap(paths.Dst(f.Dst), root) {
				errs = append(errs, fmt.Errorf("%s: %q is inside a config repo, where a private key could get committed", what, f.Dst))
			}
		}
		if len(f.Keys) == 0 {
			errs = append(errs, fmt.Errorf("%s: keys is required", what))
		}
		seen := map[string]bool{}
		for j, k := range f.Keys {
			key := fmt.Sprintf("%s.keys[%d]", what, j)
			if k.Bitwarden == "" {
				errs = append(errs, fmt.Errorf("%s: bitwarden is required", key))
			}
			if err := checkPublicKey(k.PublicKey); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", key, err))
			} else if seen[k.PublicKey] {
				errs = append(errs, fmt.Errorf("%s: publicKey %s is listed twice", key, k.PublicKey))
			}
			seen[k.PublicKey] = true
		}
	}
	return errs
}

// checkPublicKey never quotes a value it rejects: a pasted private key would
// otherwise land in the error message.
func checkPublicKey(key string) error {
	switch {
	case key == "":
		return errors.New("publicKey is required")
	case strings.HasPrefix(strings.ToUpper(key), "AGE-SECRET-KEY-"):
		return errors.New("publicKey holds a private key; remove it from the config and rotate it if it was committed")
	}
	if _, err := age.ParseX25519Recipient(key); err != nil {
		return errors.New("publicKey isn't an age X25519 public key (age1...)")
	}
	return nil
}
