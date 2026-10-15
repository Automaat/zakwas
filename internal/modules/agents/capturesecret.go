package agents

import (
	"net/url"
	"regexp"
	"strings"
)

const credentialReason = "looks like it holds a credential, which would reach the config repo; add it to zakwas.yaml by hand"

var (
	tokenPrefix    = regexp.MustCompile(`(^|[^A-Za-z0-9])(ghp_|gho_|ghs_|ghu_|github_pat_|glpat-|xox[abprs]-|sk-[A-Za-z0-9]{8}|AKIA[0-9A-Z]{8})`)
	credentialName = regexp.MustCompile(`(?i)(token|secret|passw(or)?d|passwd|pwd|api[-_]?key|access[-_]?key|private[-_]?key|credential|auth|bearer|cookie|(^|[-_])key$|^key)`)
	credentialWord = regexp.MustCompile(`(?i)^(login|password|passwd|token|secret|bearer|basic)$`)
	authHeader     = regexp.MustCompile(`(?i)^(authorization|proxy-authorization|x-api-key|api-key)[:=]?`)
	urlPassword    = regexp.MustCompile(`://[^/\s@:]+:[^/\s@]+@`)
	urlQuery       = regexp.MustCompile(`[a-z][a-z0-9+.-]*://[^\s?]*\?\S*=`)
	randomToken    = regexp.MustCompile(`^[A-Za-z0-9_-]{24,}$`)
	shortPassword  = regexp.MustCompile(`^-p[^-\s]\S{3,}$`)
	plainSegment   = regexp.MustCompile(`^([a-z][a-z_.-]*|v[0-9]+)$`)
)

// looksSecret reports whether command words name a credential together with
// its value, or hold one by its look. It errs on the side of skipping: a
// finding that is skipped is declared by hand, one that isn't reaches the
// config repo. Words with spaces, such as `sh -c "KEY=x srv"`, are checked
// piece by piece as well.
func looksSecret(words []string) bool {
	var tokens []string
	for _, w := range words {
		tokens = append(tokens, w)
		if f := strings.Fields(w); len(f) > 1 {
			tokens = append(tokens, f...)
		}
	}
	for i, t := range tokens {
		hasNext := i+1 < len(tokens)
		name, value, assigned := strings.Cut(strings.TrimLeft(t, "-"), "=")
		switch {
		case tokenPrefix.MatchString(t), urlPassword.MatchString(t), urlQuery.MatchString(t), shortPassword.MatchString(t):
			return true
		case secretURLPath(t):
			return true
		case randomToken.MatchString(t) && strings.ContainsAny(t, "0123456789") && strings.ContainsAny(strings.ToLower(t), "abcdefghijklmnopqrstuvwxyz"):
			return true
		case authHeader.MatchString(t) && (len(t) > len(authHeader.FindString(t)) || hasNext):
			return true
		case strings.HasPrefix(t, "-") && credentialName.MatchString(name) && (assigned && value != "" || !assigned && hasNext):
			return true
		case !strings.HasPrefix(t, "-") && assigned && value != "" && credentialName.MatchString(name):
			return true
		case credentialWord.MatchString(t) && hasNext && !strings.HasPrefix(tokens[i+1], "-"):
			return true
		case (t == "-H" || t == "--header" || t == "--headers") && hasNext && credentialName.MatchString(tokens[i+1]):
			return true
		}
	}
	return false
}

// secretURLPath reports an http(s) URL whose path has a segment that isn't a
// plain lower-case word or version, such as a key in /KEY/mcp.
func secretURLPath(t string) bool {
	if !strings.HasPrefix(t, "http://") && !strings.HasPrefix(t, "https://") {
		return false
	}
	u, err := url.Parse(t)
	if err != nil {
		return true
	}
	for _, seg := range strings.Split(strings.Trim(u.Path, "/"), "/") {
		if seg != "" && !plainSegment.MatchString(seg) {
			return true
		}
	}
	return false
}
