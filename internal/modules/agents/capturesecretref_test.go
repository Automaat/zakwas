package agents

// refLooksSecret is origin/main's credential screen, kept to prove the current one only ever skips as much.

import (
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

var (
	refTokenPrefix = regexp.MustCompile(`(^|[^A-Za-z0-9])(ghp_|gho_|ghs_|ghu_|ghr_|github_pat_|glpat-|xox[abprs]-|sk-[A-Za-z0-9]{8}|sk_live_|rk_live_|AKIA[0-9A-Z]{8}|ASIA[0-9A-Z]{8}|AIza[0-9A-Za-z_-]{20}|npm_[A-Za-z0-9]{20})`)
	refJwtShape    = regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*`)
	refBase64Pad   = regexp.MustCompile(`^[A-Za-z0-9+/]{16,}={1,2}$`)
	refUuidShape   = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)
	refUpperID     = regexp.MustCompile(`^[A-Z0-9]{8,}$`)
	refHexBlob     = regexp.MustCompile(`^[0-9a-fA-F]{32,}$`)
	refPlainBlob   = regexp.MustCompile(`^[A-Za-z0-9_]{24,}$`)
	refLongToken   = regexp.MustCompile(`^[A-Za-z0-9+/=_.-]{24,}$`)
	refJsonKey     = regexp.MustCompile(`(?i)["'][^"']*(pass|pwd|secret|token|key|cred|auth)[^"']*["']\s*:`)
	refUrlWord     = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://\S+`)
	refAssignment  = regexp.MustCompile(`^[-/$]{0,2}(?:env:)?([A-Za-z_][A-Za-z0-9_.-]*)[:=](.*)$`)
	refShortPass   = regexp.MustCompile(`^-p[^-\s]\S{3,}$`)
	refCredName    = regexp.MustCompile(`(?i)(pass|pwd|pat([^h]|$)|key|token|secret|cred|auth|bearer|cookie)`)
	refBareName    = regexp.MustCompile(`(?i)(pass|pwd|secret|token|cred|cookie|[-_.]key|key[-_.]|^key$|apikey|bearer)`)
)

var refCredentialFlags = map[string]bool{
	"-p": true, "-P": true, "-k": true, "-t": true, "-w": true, "--pw": true, "--pass": true, "--passphrase": true,
	"--pin": true, "--session-id": true, "--userpass": true, "--login": true, "/p": true, "/pw": true,
}

var refUserFlags = map[string]bool{"-u": true, "--user": true, "--proxy-user": true, "/user": true}

func refLooksSecret(words []string) bool {
	for _, w := range words {
		if refJwtShape.MatchString(w) || refJsonKey.MatchString(w) || refTokenPrefix.MatchString(w) || refUrlSecret(w) {
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
		if refTokenLike(t) || refShortPass.MatchString(t) || strings.Contains(strings.ToLower(t), "authorization") {
			return true
		}
		if m := refAssignment.FindStringSubmatch(t); m != nil && m[2] != "" && refCredName.MatchString(m[1]) && !strings.EqualFold(m[1], "path") {
			return true
		}
		switch {
		case refCredentialFlags[t] && next != "":
			return true
		case refUserFlags[t] && strings.Contains(next, ":"):
			return true
		case (t == "-H" || t == "--header" || t == "--headers") && refCredName.MatchString(next):
			return true
		case strings.HasPrefix(t, "-") && !strings.Contains(t, "=") && refCredName.MatchString(t) && !strings.HasSuffix(t, "-path") && next != "" && !strings.HasPrefix(next, "-"):
			return true
		case !strings.HasPrefix(t, "-") && refBareName.MatchString(t) && next != "" && !strings.HasPrefix(next, "-") && t != "auth":
			return true
		case strings.EqualFold(t, "login") && next != "" && !strings.HasPrefix(next, "-"):
			return true
		}
	}
	return false
}

func refTokenLike(t string) bool {
	switch {
	case refBase64Pad.MatchString(t), refHexBlob.MatchString(t), refUuidShape.MatchString(t):
		return true
	case refLongToken.MatchString(t):
		return refMixed(t) || refPlainBlob.MatchString(t) && strings.ContainsAny(t, "0123456789") && strings.ContainsFunc(t, unicode.IsLetter)
	}
	return false
}

func refMixed(s string) bool {
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

func refUrlSecret(text string) bool {
	for _, raw := range refUrlWord.FindAllString(text, -1) {
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
			if len(seg) >= 20 && refMixedOrBlob(seg) || refUpperID.MatchString(seg) {
				return true
			}
		}
	}
	return false
}

func refMixedOrBlob(seg string) bool {
	return refMixed(seg) || refHexBlob.MatchString(seg) || refBase64Pad.MatchString(seg)
}
