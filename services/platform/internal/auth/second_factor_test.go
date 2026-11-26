package auth

import "testing"

func TestAuthenticatorShaped(t *testing.T) {
	for code, want := range map[string]bool{
		"123456":                     true,
		" 123456\n":                  true,
		"12345":                      false,
		"1234567":                    false,
		"123 456":                    false,
		"12345a":                     false,
		"١٢٣٤٥٦":                     false,
		"ABCDEFGHIJKLMNOPQRSTUVWXYZ": false,
		"":                           false,
	} {
		if got := authenticatorShaped(code); got != want {
			t.Errorf("authenticatorShaped(%q) = %v, want %v", code, got, want)
		}
	}
}
