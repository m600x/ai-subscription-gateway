package provider

import (
	"strings"
	"testing"
)

func TestNewID(t *testing.T) {
	a := NewID()
	if !strings.HasPrefix(a, "chatcmpl-") {
		t.Errorf("id %q lacks chatcmpl- prefix", a)
	}
	if len(a) != len("chatcmpl-")+24 { // 12 random bytes hex-encoded
		t.Errorf("id %q has unexpected length %d", a, len(a))
	}
	if b := NewID(); a == b {
		t.Errorf("consecutive ids collided: %q", a)
	}
}
