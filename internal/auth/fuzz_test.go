package auth

import (
	"encoding/base64"
	"strings"
	"testing"
)

func FuzzCanonicalJWT(f *testing.F) {
	for _, seed := range []string{
		"",
		"e30.e30.AA",
		"e30.e30.AB",
		"e30.e30.AA=",
		"e30.e30.AA\n",
		"e30..AA",
		"e30.e30.AA.extra",
		"\xff\x00",
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, token string) {
		if len(token) > maxJWTTokenBytes {
			t.Skip()
		}
		if canonicalJWT(token) {
			parts := strings.Split(token, ".")
			if len(parts) != 3 {
				t.Fatal("accepted a token without three segments")
			}
			for _, part := range parts {
				if part == "" || strings.ContainsAny(part, "=\r\n") {
					t.Fatal("accepted an empty, padded, or multiline segment")
				}
			}
			// Alternative spellings must not reach the signature verifier.
			for _, suffix := range []string{"=", "\n", "."} {
				if canonicalJWT(token + suffix) {
					t.Fatalf("accepted a token after appending %q", suffix)
				}
			}
		}

		// Every nonempty byte string can also supply a canonical segment.
		// This exercises successful decoding even when random tokens are invalid.
		if token != "" {
			segment := base64.RawURLEncoding.EncodeToString([]byte(token))
			if !canonicalJWT("e30.e30." + segment) {
				t.Fatal("rejected a token constructed with canonical base64url encoding")
			}
		}
	})
}
