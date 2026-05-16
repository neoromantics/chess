package auth

// Password-strength validation, NIST 800-63B-aligned.
//
// We deliberately do NOT require character-class diversity (upper +
// lower + digit + symbol). That heuristic produces predictable patterns
// (P@ssw0rd1!) without buying entropy; NIST removed the requirement in
// 800-63B for that reason. Instead we:
//   - require length >= 8 (the NIST floor)
//   - cap at 128 (anything longer is a paste accident or a DoS attempt
//     on bcrypt)
//   - reject if it appears verbatim in the small embedded list of
//     historically-most-breached passwords
//   - reject if it equals (or contains) the username, which is the most
//     common "they re-used something obvious" failure mode
//
// Heavyweight zxcvbn-style scoring would catch more edge cases but
// requires either an embedded dictionary (~MB) or a remote API call;
// neither is worth it at this scale. The blacklist below covers the
// passwords that show up in every credential-stuffing dump.

import (
	"errors"
	"strings"
	"unicode/utf8"
)

const (
	minPasswordLength = 8
	// 128 chars is well above any humanly-typed password but small
	// enough that bcrypt's per-hash CPU stays bounded. bcrypt itself
	// truncates at 72 bytes — anything beyond that is wasted bits.
	maxPasswordLength = 128
)

// commonPasswords is a small embedded blacklist of the passwords that
// appear most often in leaked credential dumps. Keep it tight — a
// 100k-entry list would cost a megabyte and only marginally improve
// coverage. Lowercased; comparison is also lowercased.
var commonPasswords = map[string]struct{}{
	"password":     {},
	"password1":    {},
	"password12":   {},
	"password123":  {},
	"passw0rd":     {},
	"p@ssw0rd":     {},
	"12345678":     {},
	"123456789":    {},
	"1234567890":   {},
	"qwerty":       {},
	"qwerty123":    {},
	"qwertyuiop":   {},
	"abc123":       {},
	"abc12345":     {},
	"letmein":      {},
	"welcome":      {},
	"welcome1":     {},
	"welcome123":   {},
	"iloveyou":     {},
	"admin":        {},
	"administrator": {},
	"root":         {},
	"changeme":     {},
	"sunshine":     {},
	"princess":     {},
	"football":     {},
	"baseball":     {},
	"dragon":       {},
	"monkey":       {},
	"master":       {},
	"trustno1":     {},
	"superman":     {},
	"batman":       {},
	"starwars":     {},
	"chess":        {},
	"chess123":     {},
	"chessmaster":  {},
}

// PasswordError is the public-facing validation result. Callers
// surface the message verbatim to users so it should describe the
// *rule violated*, not internal jargon.
type PasswordError struct{ Message string }

func (e *PasswordError) Error() string { return e.Message }

// ValidatePassword returns nil if the password meets policy, or a
// *PasswordError with a user-facing explanation otherwise. Pass the
// username so we can reject re-use of it as the password (the most
// common weak choice after "password" itself).
func ValidatePassword(password, username string) error {
	if utf8.RuneCountInString(password) < minPasswordLength {
		return &PasswordError{Message: "password must be at least 8 characters"}
	}
	if utf8.RuneCountInString(password) > maxPasswordLength {
		return &PasswordError{Message: "password is too long (128 character max)"}
	}

	lower := strings.ToLower(password)
	if _, ok := commonPasswords[lower]; ok {
		return &PasswordError{Message: "password is too common — pick something less guessable"}
	}

	// Username re-use (and substring re-use, since "alice2026" with
	// username "alice" is still weak). Only check when the username
	// is non-trivially long — for 1-2 char usernames every password
	// would match and the message would be confusing.
	if username != "" && utf8.RuneCountInString(username) >= 3 {
		if strings.Contains(lower, strings.ToLower(username)) {
			return &PasswordError{Message: "password must not contain your username"}
		}
	}

	return nil
}

// IsPasswordError reports whether err came from ValidatePassword. Lets
// callers distinguish user-input failures from internal errors when
// rendering HTTP responses.
func IsPasswordError(err error) bool {
	var pe *PasswordError
	return errors.As(err, &pe)
}
