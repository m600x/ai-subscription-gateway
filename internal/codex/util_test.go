package codex

import (
	"encoding/base64"
	"encoding/json"
	"testing"
	"time"
)

// makeJWT builds an unsigned JWT with the given claims (test helper).
func makeJWT(claims map[string]any) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	pj, _ := json.Marshal(claims)
	p := base64.RawURLEncoding.EncodeToString(pj)
	return h + "." + p + ".sig"
}

func TestAccountIDFromIDToken(t *testing.T) {
	tok := makeJWT(map[string]any{
		"https://api.openai.com/auth": map[string]any{"chatgpt_account_id": "acc_123"},
	})
	if got := accountIDFromIDToken(tok); got != "acc_123" {
		t.Errorf("account id = %q, want acc_123", got)
	}
	if got := accountIDFromIDToken("not-a-jwt"); got != "" {
		t.Errorf("bad token should yield empty account, got %q", got)
	}
}

func TestTokenExpiry(t *testing.T) {
	exp := time.Now().Add(time.Hour).Unix()
	tok := makeJWT(map[string]any{"exp": exp})
	got := tokenExpiry(tok)
	if got.Unix() != exp {
		t.Errorf("exp = %d, want %d", got.Unix(), exp)
	}
	if !tokenExpiry("garbage").IsZero() {
		t.Error("bad token should yield zero expiry")
	}
}

func TestArgString(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"json string", `"{\"city\":\"paris\"}"`, `{"city":"paris"}`},
		{"object", `{"city":"paris"}`, `{"city":"paris"}`},
		{"empty", ``, "{}"},
		{"empty string", `""`, "{}"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := argString([]byte(tc.in)); got != tc.want {
				t.Errorf("argString(%s) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestNewUUIDShape(t *testing.T) {
	u := newUUID()
	if len(u) != 36 || u[14] != '4' {
		t.Errorf("uuid v4 malformed: %q", u)
	}
}
