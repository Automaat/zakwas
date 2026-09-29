package selfupdate

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/Automaat/zakwas/internal/runner"
)

// AttestedSince is the first release published with build provenance
// attestations; older ones can only be checked against checksums.txt.
const AttestedSince = "0.4.0"

// Attested reports whether version was released with attestations. A
// pre-release counts as its release version, like install.sh.
func Attested(version string) bool {
	have, want := versionParts(version), versionParts(AttestedSince)
	for i := range want {
		if have[i] != want[i] {
			return have[i] > want[i]
		}
	}
	return true
}

func versionParts(v string) [3]int {
	v, _, _ = strings.Cut(v, "-")
	v, _, _ = strings.Cut(v, "+")
	var parts [3]int
	for i, p := range strings.SplitN(v, ".", 3) {
		parts[i], _ = strconv.Atoi(p)
	}
	return parts
}

// AttestationArgs are the `gh attestation verify` options pinning the
// attestation to this repo, its release workflow, and the version's tag.
func AttestationArgs(version string) []string {
	return []string{
		"--repo", "Automaat/zakwas",
		"--signer-workflow", "Automaat/zakwas/.github/workflows/release.yml",
		"--source-ref", "refs/tags/v" + version,
	}
}

// CanVerify reports whether gh is installed, logged in, and new enough to
// support --source-ref, the same test install.sh makes.
func CanVerify(ctx context.Context, r runner.Runner) bool {
	if !r.Installed("gh") {
		return false
	}
	if res, err := r.Run(ctx, runner.Cmd{Name: "gh", Args: []string{"auth", "status"}}); err != nil || res.ExitCode != 0 {
		return false
	}
	res, err := r.Run(ctx, runner.Cmd{Name: "gh", Args: []string{"attestation", "verify", "--help"}})
	return err == nil && res.ExitCode == 0 && strings.Contains(res.Stdout+res.Stderr, "--source-ref")
}

// VerifyAttestation checks the build provenance of the archive at file.
func VerifyAttestation(ctx context.Context, r runner.Runner, file, version string) error {
	cmd := runner.Cmd{Name: "gh", Args: append([]string{"attestation", "verify", file}, AttestationArgs(version)...)}
	if err := runner.Check(ctx, r, cmd); err != nil {
		return fmt.Errorf("attestation verification failed: %w", err)
	}
	return nil
}
