package secrets

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/Automaat/zakwas/internal/config"
	"github.com/Automaat/zakwas/internal/runner"
)

const gpg = "gpg"

// gpgMissing lists the secrets.gpg entries whose key the keyring doesn't hold
// as a usable secret. Without gpg nothing is held, so every entry is missing.
func (m *Module) gpgMissing(ctx context.Context) ([]config.GPGKey, error) {
	if len(m.Secrets.GPG) == 0 {
		return nil, nil
	}
	if !m.Runner.Installed(gpg) {
		return slices.Clone(m.Secrets.GPG), nil
	}
	have, err := m.softwareKeys(ctx)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(slices.Clone(m.Secrets.GPG), func(k config.GPGKey) bool {
		return have[strings.ToUpper(k.Fingerprint)]
	}), nil
}

// softwareKeys is the set of fingerprints the keyring holds a secret for.
// gpg's own output never reaches an error.
func (m *Module) softwareKeys(ctx context.Context) (map[string]bool, error) {
	cmd := runner.Cmd{Name: gpg, Args: []string{"--batch", "--list-secret-keys", "--with-colons", "--with-subkey-fingerprints"}}
	res, err := m.Runner.Run(ctx, cmd)
	if err != nil {
		return nil, fmt.Errorf("secrets: %w", err)
	}
	if res.ExitCode != 0 {
		return nil, fmt.Errorf("secrets: gpg --list-secret-keys: exit %d", res.ExitCode)
	}
	return parseSecretKeys(res.Stdout), nil
}

// parseSecretKeys reads gpg's colon listing. A key counts only when its secret
// is in the keyring: field 15 of a sec or ssb record is '+' for a key kept in
// the keyring (empty in older gpg), '#' for a stub without the secret, and the
// smartcard's serial number for a key that lives on a card.
func parseSecretKeys(listing string) map[string]bool {
	have := map[string]bool{}
	usable := false
	for line := range strings.Lines(listing) {
		fields := strings.Split(strings.TrimRight(line, "\r\n"), ":")
		switch fields[0] {
		case "sec", "ssb":
			usable = len(fields) < 15 || fields[14] == "" || fields[14] == "+"
		case "pub", "sub":
			usable = false
		case "fpr":
			if usable && len(fields) > 9 {
				have[strings.ToUpper(fields[9])] = true
			}
		}
	}
	return have
}

// restoreGPG imports a note and then checks the keyring, since gpg exits 0 for
// a note that holds only a public key. A note's content and gpg's output stay
// out of every error.
func (m *Module) restoreGPG(ctx context.Context, key config.GPGKey, session []string) error {
	res, err := m.Runner.Run(ctx, runner.Cmd{Name: bw, Args: []string{"get", "notes", key.Bitwarden}, Env: session})
	if err != nil {
		return fmt.Errorf("secrets: %w", err)
	}
	if res.ExitCode != 0 {
		return fmt.Errorf("secrets: can't read Bitwarden note %q: %s", key.Bitwarden, strings.TrimSpace(res.Stderr))
	}
	if strings.TrimSpace(res.Stdout) == "" {
		return fmt.Errorf("secrets: Bitwarden note %q is empty", key.Bitwarden)
	}
	imported, err := m.Runner.Run(ctx, runner.Cmd{Name: gpg, Args: []string{"--batch", "--import"}, Stdin: res.Stdout})
	if err != nil {
		return fmt.Errorf("secrets: %w", err)
	}
	if imported.ExitCode != 0 {
		return fmt.Errorf("secrets: gpg couldn't import Bitwarden note %q: exit %d", key.Bitwarden, imported.ExitCode)
	}
	have, err := m.softwareKeys(ctx)
	if err != nil {
		return err
	}
	if !have[strings.ToUpper(key.Fingerprint)] {
		return fmt.Errorf("secrets: Bitwarden note %q was imported but the keyring still has no secret key %s", key.Bitwarden, strings.ToUpper(key.Fingerprint))
	}
	return nil
}

// gpgTarget names an entry by the last 16 hex characters of its fingerprint.
func gpgTarget(key config.GPGKey) string {
	fpr := strings.ToUpper(key.Fingerprint)
	return "gpg key " + fpr[len(fpr)-16:]
}
