package sessions

import (
	"regexp"
	"strings"
	"unicode"
)

// Redaction markers are [REDACTED:<kind>]; the model is told they stand for removed secrets.
const redactedPrefix = "[REDACTED:"

func marker(kind string) string { return redactedPrefix + kind + "]" }

var (
	// A PEM block up to its END line, or to the end of the text when it was cut off.
	privateKeyRE = regexp.MustCompile(`(?s)-----BEGIN [A-Z0-9 ]*KEY(?: BLOCK)?-----.*?(?:-----END [A-Z0-9 ]*KEY(?: BLOCK)?-----|\z)`)
	// Authorization: Bearer x, "Authorization": "Basic x", authorization=x.
	authorizationRE = regexp.MustCompile(`(?i)(\bauthorization["']?\s*[:=]\s*["']?)(?:(?:bearer|basic|token|digest)\s+)?[^\s"',;]+`)
	privateTokenRE  = regexp.MustCompile(`(?i)(\bprivate-token["']?\s*[:=]\s*["']?)[^\s"',;]+`)
	// key=value or key: value where the key names a password, secret, token or API key
	// (DB_PASSWORD=x, "api_key": "x", client_secret: x). $VARS and {{ templates }} are
	// references, not values, and stay.
	assignmentRE = regexp.MustCompile(`(?i)(\b[\w.-]*(?:password|passwd|secret|token|api[_-]?key)[\w.-]*["']?\s*(?:=|:)\s*)("[^"\n]*"|'[^'\n]*'|[^\s"',;&]+)`)
	// --password x, --token=x on a command line.
	secretFlagRE = regexp.MustCompile(`(?i)(--[\w-]*(?:password|passwd|secret|token|api-key)[\w-]*(?:=|\s+))("[^"\n]*"|'[^'\n]*'|[^\s"'-][^\s"';&|]*)`)
	// scheme://user:pass@host keeps the scheme and host.
	userinfoRE = regexp.MustCompile(`(?i)\b([a-z][a-z0-9+.-]*://)[^/\s@"']+@`)
	jwtRE      = regexp.MustCompile(`\beyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`)
	hexRE      = regexp.MustCompile(`\b[0-9A-Fa-f]{32,}\b`)
	base64RE   = regexp.MustCompile(`[A-Za-z0-9+/_-]{32,}={0,2}`)
)

// Redact replaces secrets with [REDACTED:<kind>]: PEM private keys, Authorization and
// PRIVATE-TOKEN values, password/secret/token/api_key assignments, URL userinfo, JWTs,
// and hex or base64 runs of 32 or more characters.
func Redact(text string) string {
	text = privateKeyRE.ReplaceAllString(text, marker("private-key"))
	text = authorizationRE.ReplaceAllString(text, "${1}"+marker("authorization"))
	text = privateTokenRE.ReplaceAllString(text, "${1}"+marker("private-token"))
	text = jwtRE.ReplaceAllString(text, marker("jwt"))
	text = userinfoRE.ReplaceAllString(text, "${1}"+marker("userinfo")+"@")
	text = redactValues(secretFlagRE, text)
	text = redactValues(assignmentRE, text)
	text = hexRE.ReplaceAllString(text, marker("hex"))
	return base64RE.ReplaceAllStringFunc(text, func(run string) string {
		if looksRandom(run) {
			return marker("base64")
		}
		return run
	})
}

// redactValues replaces the value (group 2) of each match of re, keeping its quotes.
func redactValues(re *regexp.Regexp, text string) string {
	return re.ReplaceAllStringFunc(text, func(match string) string {
		parts := re.FindStringSubmatch(match)
		value, quote := parts[2], ""
		if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') {
			value, quote = value[1:len(value)-1], value[:1]
		}
		if value == "" || isReference(value) {
			return match
		}
		return parts[1] + quote + marker("secret") + quote
	})
}

// isReference reports whether an assigned value names a secret instead of holding one:
// a shell or template variable, or a value already redacted.
func isReference(value string) bool {
	return strings.HasPrefix(value, "$") || strings.HasPrefix(value, "{{") || strings.HasPrefix(value, "%(") ||
		strings.HasPrefix(value, "<") || strings.HasPrefix(value, redactedPrefix)
}

// looksRandom tells encoded secrets from long paths and identifiers, which share
// base64's alphabet: random base64 mixes digits with upper and lower case letters in
// similar numbers, while paths (github.com/JB0t/clctl/internal) and CamelCase names are
// mostly lower case.
func looksRandom(run string) bool {
	var upper, lower, digits int
	for _, char := range run {
		switch {
		case unicode.IsUpper(char):
			upper++
		case unicode.IsLower(char):
			lower++
		case unicode.IsDigit(char):
			digits++
		}
	}
	letters := upper + lower
	if digits == 0 || upper == 0 || lower == 0 {
		return false
	}
	ratio := float64(upper) / float64(letters)
	return ratio >= 0.25 && ratio <= 0.75
}
