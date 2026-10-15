package agents

import (
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

const credentialReason = "may hold a credential, which would reach the config repo; add it to zakwas.yaml by hand"

// The screening is a fail-safe, not a proof: a finding that is skipped is
// declared by hand, one that isn't reaches the config repo. It names what a
// credential looks like or what it is passed with. A lower-case word-shaped
// key given without any such name can't be told from an ordinary argument.
var (
	tokenPrefix = regexp.MustCompile(`(^|[^A-Za-z0-9])(ghp_|gho_|ghs_|ghu_|ghr_|github_pat_|glpat-|xox[abprs]-|sk-[A-Za-z0-9]{8}|sk_live_|rk_live_|AKIA[0-9A-Z]{8}|ASIA[0-9A-Z]{8}|AIza[0-9A-Za-z_-]{20}|npm_[A-Za-z0-9]{20})`)
	jwtShape    = regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*`)
	base64Pad   = regexp.MustCompile(`^[A-Za-z0-9+/]{16,}={1,2}$`)
	uuidShape   = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	upperID     = regexp.MustCompile(`^[A-Z0-9]{8,}$`)
	hexBlob     = regexp.MustCompile(`^[0-9a-fA-F]{32,}$`)
	plainBlob   = regexp.MustCompile(`^[A-Za-z0-9_]{24,}$`)
	longToken   = regexp.MustCompile(`^[A-Za-z0-9+/=_.-]{24,}$`)
	jsonKey     = regexp.MustCompile(`(?i)["'][^"']*(pass|pwd|secret|token|key|cred|auth)[^"']*["']\s*:`)
	urlWord     = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://\S+`)
	assignment  = regexp.MustCompile(`^[-/$]{0,2}(?:env:)?([A-Za-z_][A-Za-z0-9_.-]*)[:=](.*)$`)
	shortPass   = regexp.MustCompile(`^-p[^-\s]\S{3,}$`)
	envIdent    = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
	plainSeg    = regexp.MustCompile(`^([a-z][a-z_.-]*|v[0-9]+)$`)
	gluedCred   = regexp.MustCompile(`^(?:[a-z]*(?:token|secret|password|passwd|passphrase)|(?:api|access|auth|secret|private|ssh|client|session|license|signing|master|encryption)key)$`)
)

var credWords = map[string]bool{
	"pass": true, "password": true, "passwords": true, "passwd": true, "pwd": true, "pw": true, "passphrase": true,
	"secret": true, "secrets": true, "token": true, "key": true, "cred": true, "creds": true, "credential": true,
	"credentials": true, "auth": true, "bearer": true, "cookie": true, "cookies": true, "pat": true,
}

// notValue ends a name whose value locates, describes or measures a
// credential instead of being one: --password-file, --key-size.
var notValue = map[string]bool{
	"file": true, "path": true, "dir": true, "directory": true, "name": true, "url": true, "uri": true, "env": true,
	"limit": true, "count": true, "size": true, "length": true, "bits": true, "type": true, "id": true, "ttl": true,
	"timeout": true, "jar": true, "fd": true, "stdin": true, "format": true, "algorithm": true,
}

// nameWords splits a flag or variable name into lower-case words at
// separators and case changes.
func nameWords(name string) []string {
	var words []string
	var cur []rune
	flush := func() {
		if len(cur) > 0 {
			words = append(words, string(cur))
			cur = nil
		}
	}
	var prev rune
	for _, r := range name {
		switch {
		case !unicode.IsLetter(r) && !unicode.IsDigit(r):
			flush()
		case unicode.IsUpper(r) && unicode.IsLower(prev):
			flush()
			cur = append(cur, unicode.ToLower(r))
		default:
			cur = append(cur, unicode.ToLower(r))
		}
		prev = r
	}
	flush()
	return words
}

// credentialName reports a name that takes a credential as its value. Whole
// words count, so --max-tokens, --tokenizer and --keyboard don't; a name
// that ends in file, path or the like doesn't either. A bare word is only a
// credential for its stronger names: auth and pat are commands and
// abbreviations there.
func credentialName(name string, bare bool) bool {
	words := nameWords(name)
	if len(words) == 0 || notValue[words[len(words)-1]] {
		return false
	}
	for _, w := range words {
		for suffix := range notValue {
			if rest, ok := strings.CutSuffix(w, suffix); ok && rest != "" && (credWords[rest] || gluedCred.MatchString(rest)) {
				return false
			}
		}
	}
	for _, w := range words {
		if bare && (w == "auth" || w == "pat") {
			continue
		}
		if credWords[w] || gluedCred.MatchString(w) {
			return true
		}
	}
	return false
}

// credentialFlags take their value as the next word.
var credentialFlags = map[string]bool{
	"-p": true, "-P": true, "-k": true, "-t": true, "-w": true, "--pw": true, "--pass": true, "--passphrase": true,
	"--pin": true, "--session-id": true, "--userpass": true, "--login": true, "/p": true, "/pw": true,
}

// userFlags take user:value as the next word.
var userFlags = map[string]bool{"-u": true, "--user": true, "--proxy-user": true, "/user": true}

// looksSecret reports whether command words name a credential together with
// its value, or hold one by their look. Words with spaces, such as
// `sh -c "KEY=x srv"`, are checked piece by piece as well.
func looksSecret(words []string) bool {
	for _, w := range words {
		if jwtShape.MatchString(w) || jsonKey.MatchString(w) || tokenPrefix.MatchString(w) || urlSecret(w) {
			return true
		}
	}
	var tokens []string
	for _, w := range words {
		for _, f := range strings.FieldsFunc(w, func(r rune) bool { return unicode.IsSpace(r) || r == ';' || r == '&' || r == '|' || r == ',' }) {
			tokens = append(tokens, strings.Trim(f, "\"'`(){}[]"))
		}
	}
	for i, t := range tokens {
		next := ""
		if i+1 < len(tokens) {
			next = tokens[i+1]
		}
		if tokenLike(t) || shortPass.MatchString(t) || strings.Contains(strings.ToLower(t), "authorization") {
			return true
		}
		if m := assignment.FindStringSubmatch(t); m != nil && m[2] != "" && credentialName(m[1], false) {
			return true
		}
		switch {
		case credentialFlags[t] && next != "":
			return true
		case userFlags[t] && strings.Contains(next, ":"):
			return true
		case (t == "-H" || t == "--header" || t == "--headers") && credentialName(next, false):
			return true
		case strings.HasPrefix(t, "-") && !strings.Contains(t, "=") && credentialName(t, false) && next != "" && !strings.HasPrefix(next, "-"):
			return true
		case !strings.HasPrefix(t, "-") && credentialName(t, true) && next != "" && !strings.HasPrefix(next, "-"):
			return true
		case strings.EqualFold(t, "login") && next != "" && !strings.HasPrefix(next, "-"):
			return true
		}
	}
	return false
}

// tokenLike reports a word that looks like a generated secret rather than a
// name or a path: a base64 blob, a long hex string, or a long mix of cases
// and digits.
func tokenLike(t string) bool {
	switch {
	case base64Pad.MatchString(t), hexBlob.MatchString(t), uuidShape.MatchString(t):
		return true
	case longToken.MatchString(t):
		return mixed(t) || plainBlob.MatchString(t) && strings.ContainsAny(t, "0123456789") && strings.ContainsFunc(t, unicode.IsLetter)
	}
	return false
}

// mixed reports upper case, lower case and a digit all in s.
func mixed(s string) bool {
	var upper, lower, digit bool
	for _, r := range s {
		switch {
		case unicode.IsUpper(r):
			upper = true
		case unicode.IsLower(r):
			lower = true
		case unicode.IsDigit(r):
			digit = true
		}
	}
	return upper && lower && digit
}

// urlSecret reports a URL in text with a password, user info other than
// git, a query, a fragment or a path segment that looks like a key.
func urlSecret(text string) bool {
	for _, raw := range urlWord.FindAllString(text, -1) {
		u, err := url.Parse(strings.Trim(raw, "\"'`),;"))
		if err != nil {
			return true
		}
		if u.User != nil {
			if _, hasPassword := u.User.Password(); hasPassword || u.User.Username() != "git" {
				return true
			}
		}
		if u.RawQuery != "" || u.Fragment != "" || strings.Contains(raw, "#") {
			return true
		}
		for _, seg := range strings.Split(strings.Trim(u.Path, "/"), "/") {
			if len(seg) >= 20 && mixedOrBlob(seg) || upperID.MatchString(seg) {
				return true
			}
		}
	}
	return false
}

func mixedOrBlob(seg string) bool {
	return mixed(seg) || hexBlob.MatchString(seg) || base64Pad.MatchString(seg)
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
		if seg != "" && !plainSeg.MatchString(seg) {
			return true
		}
	}
	return false
}

// validEnvName reports a name that is safe to write as an environment
// variable reference: upper case, as such names are by convention, which a
// key given as a name is not, and not a credential in disguise.
func validEnvName(name string) bool {
	return envIdent.MatchString(name) && !looksSecret([]string{name}) && !tokenLike(name)
}
