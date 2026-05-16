package auth

import "testing"

func TestValidatePassword(t *testing.T) {
	cases := []struct {
		name     string
		password string
		username string
		ok       bool
		// substring of error message; empty when ok=true
		errSub string
	}{
		{"valid", "correct horse battery staple", "alice", true, ""},
		{"valid min length", "abcdefgh", "alice", true, ""},
		{"too short", "abc123", "alice", false, "at least 8"},
		{"common - password", "password", "alice", false, "too common"},
		{"common - 12345678", "12345678", "alice", false, "too common"},
		{"common - mixed case", "Password", "alice", false, "too common"},
		{"contains username", "alice2026", "alice", false, "username"},
		{"contains username case-insensitive", "ALICE_pass1", "alice", false, "username"},
		// 2-char username doesn't trigger substring check (too noisy)
		{"short username no substring rule", "altruistic", "al", true, ""},
		{"too long", repeat("x", 129), "alice", false, "too long"},
		{"exactly max", repeat("x", 128), "alice", true, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidatePassword(tc.password, tc.username)
			if tc.ok && err != nil {
				t.Fatalf("expected ok, got error: %v", err)
			}
			if !tc.ok {
				if err == nil {
					t.Fatalf("expected error, got nil")
				}
				if !IsPasswordError(err) {
					t.Fatalf("expected *PasswordError, got %T: %v", err, err)
				}
				if !contains(err.Error(), tc.errSub) {
					t.Fatalf("expected error to mention %q, got %q", tc.errSub, err.Error())
				}
			}
		})
	}
}

func repeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}

func contains(s, sub string) bool {
	if sub == "" {
		return true
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
