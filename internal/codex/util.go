package codex

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Error is a typed upstream failure implementing provider.HTTPError.
type Error struct {
	Status  int
	Type    string
	Message string
}

func (e *Error) Error() string {
	return fmt.Sprintf("openai %d %s: %s", e.Status, e.Type, e.Message)
}

// HTTPStatus implements provider.HTTPError.
func (e *Error) HTTPStatus() int { return e.Status }

// ErrType implements provider.HTTPError.
func (e *Error) ErrType() string { return e.Type }

// jwtClaims decodes (without verifying) the claims segment of a JWT. The token
// is one we obtained ourselves over TLS, so we trust it and only read claims.
func jwtClaims(token string) (map[string]any, error) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return nil, fmt.Errorf("not a JWT")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		// Some tokens pad the segment; try standard decoding as a fallback.
		if raw, err = base64.StdEncoding.DecodeString(parts[1]); err != nil {
			return nil, err
		}
	}
	var claims map[string]any
	if err := json.Unmarshal(raw, &claims); err != nil {
		return nil, err
	}
	return claims, nil
}

// accountIDFromIDToken extracts the ChatGPT account id from an id_token's
// claims (claim path: https://api.openai.com/auth -> chatgpt_account_id).
func accountIDFromIDToken(idToken string) string {
	claims, err := jwtClaims(idToken)
	if err != nil {
		return ""
	}
	auth, ok := claims["https://api.openai.com/auth"].(map[string]any)
	if !ok {
		return ""
	}
	if id, ok := auth["chatgpt_account_id"].(string); ok {
		return id
	}
	return ""
}

// tokenExpiry reads the `exp` claim from an access token; zero time if absent.
func tokenExpiry(accessToken string) time.Time {
	claims, err := jwtClaims(accessToken)
	if err != nil {
		return time.Time{}
	}
	if exp, ok := claims["exp"].(float64); ok {
		return time.Unix(int64(exp), 0)
	}
	return time.Time{}
}

// newUUID returns a random RFC-4122 v4 UUID string (stdlib only).
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = (b[6] & 0x0f) | 0x40 // version 4
	b[8] = (b[8] & 0x3f) | 0x80 // variant 10
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// argString normalizes a Responses function_call arguments field (which may be
// a JSON string or an object) into the JSON string OpenAI tool_calls expect.
func argString(raw json.RawMessage) string {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return "{}"
	}
	// If it's a JSON string, its value is already the arguments JSON.
	if trimmed[0] == '"' {
		var s string
		if err := json.Unmarshal(trimmed, &s); err == nil {
			if strings.TrimSpace(s) == "" {
				return "{}"
			}
			return s
		}
	}
	// Otherwise it's an object/array/scalar: compact it.
	var buf bytes.Buffer
	if err := json.Compact(&buf, trimmed); err != nil {
		return "{}"
	}
	return buf.String()
}
