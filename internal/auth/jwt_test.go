package auth

import (
	"crypto/rand"
	"crypto/rsa"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestCheckDelegationChecksClaimsNotSignature(t *testing.T) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	claims := jwt.MapClaims{"cluster_id": "cluster-id", "iss": "node-a", "sub": "alice", "exp": time.Now().Add(time.Hour).Unix(), "token_use": "access"}
	raw, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(key)
	if err != nil {
		t.Fatal(err)
	}
	// A deliberately corrupted signature still decodes. The daemon, not this
	// local check, must refuse this credential before it becomes an Identity.
	parts := strings.Split(raw, ".")
	raw = parts[0] + "." + parts[1] + ".aW52YWxpZA"
	delegation, err := CheckDelegation(raw, "", "")
	if err != nil || delegation.ClusterID != "cluster-id" || delegation.Subject != "alice" || delegation.Issuer != "node-a" {
		t.Fatalf("unexpected delegation: %+v, %v", delegation, err)
	}
	if _, err := CheckDelegation(raw, "cluster-id", "node-a"); err != nil {
		t.Fatal("matching native target rejected")
	}
	if _, err := CheckDelegation(raw, "other-cluster", ""); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("native target override accepted")
	}
	if _, err := CheckDelegation(raw, "cluster-id", "node-b"); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("native node override accepted")
	}
	for name, change := range map[string]func(jwt.MapClaims){
		"no cluster":      func(c jwt.MapClaims) { delete(c, "cluster_id") },
		"no issuer":       func(c jwt.MapClaims) { delete(c, "iss") },
		"no subject":      func(c jwt.MapClaims) { delete(c, "sub") },
		"no expiry":       func(c jwt.MapClaims) { delete(c, "exp") },
		"expired":         func(c jwt.MapClaims) { c["exp"] = time.Now().Add(-time.Second).Unix() },
		"not active":      func(c jwt.MapClaims) { c["nbf"] = time.Now().Add(time.Hour).Unix() },
		"refresh":         func(c jwt.MapClaims) { c["token_use"] = "refresh" },
		"invalid subject": func(c jwt.MapClaims) { c["sub"] = "alice\nroot" },
	} {
		t.Run(name, func(t *testing.T) {
			candidate := jwt.MapClaims{}
			for k, v := range claims {
				candidate[k] = v
			}
			change(candidate)
			raw, err := jwt.NewWithClaims(jwt.SigningMethodRS256, candidate).SignedString(key)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := CheckDelegation(raw, "", ""); !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("got %v", err)
			}
		})
	}
	for _, raw := range []string{"", "bad", strings.Repeat("x", (16<<10)+1)} {
		if _, err := CheckDelegation(raw, "", ""); !errors.Is(err, ErrInvalidToken) {
			t.Fatalf("got %v", err)
		}
	}
	raw, err = jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("not-a-native-signing-key"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CheckDelegation(raw, "", ""); !errors.Is(err, ErrInvalidToken) {
		t.Fatal("non-native algorithm accepted")
	}
}
