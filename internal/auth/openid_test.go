package auth

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestOpenIDDelegationRequiresTargetAndClaims(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sign := func(change func(jwt.MapClaims, map[string]any)) string {
		claims := jwt.MapClaims{"iss": "https://idp.example.test/", "sub": "opaque-subject", "aud": "cluster-client", "exp": time.Now().Add(time.Hour).Unix(), "preferred_username": "alice"}
		token := jwt.NewWithClaims(jwt.SigningMethodES256, claims)
		token.Header["kid"] = "signing-key"
		if change != nil {
			change(claims, token.Header)
		}
		raw, err := token.SignedString(key)
		if err != nil {
			t.Fatal(err)
		}
		return raw
	}
	raw := sign(nil)
	delegation, err := CheckDelegation(raw, "cluster-a", "node-b")
	if err != nil || delegation.ClusterID != "cluster-a" || delegation.Subject != "opaque-subject" || delegation.Issuer != "https://idp.example.test/" {
		t.Fatalf("delegation=%+v err=%v", delegation, err)
	}
	parts := strings.Split(raw, ".")
	if _, err := CheckDelegation(parts[0]+"."+parts[1]+".aW52YWxpZA", "cluster-a", "node-b"); err != nil {
		t.Fatal("signature verification must be delegated to MCP/daemon")
	}
	for _, target := range []string{"", " padded ", "bad\ncluster", strings.Repeat("x", 257)} {
		if _, err := CheckDelegation(raw, target, "node-b"); !errors.Is(err, ErrInvalidToken) {
			t.Fatal("missing or invalid target accepted")
		}
	}
	for _, node := range []string{"", " padded ", "bad\nnode", "node-a,node-b", strings.Repeat("x", 257)} {
		if _, err := CheckDelegation(raw, "cluster-a", node); !errors.Is(err, ErrInvalidToken) {
			t.Fatal("missing or invalid node accepted")
		}
	}
	for name, change := range map[string]func(jwt.MapClaims, map[string]any){
		"no issuer":                            func(c jwt.MapClaims, _ map[string]any) { delete(c, "iss") },
		"no subject":                           func(c jwt.MapClaims, _ map[string]any) { delete(c, "sub") },
		"no audience":                          func(c jwt.MapClaims, _ map[string]any) { delete(c, "aud") },
		"invalid audience":                     func(c jwt.MapClaims, _ map[string]any) { c["aud"] = 123 },
		"empty audience":                       func(c jwt.MapClaims, _ map[string]any) { c["aud"] = []string{} },
		"invalid audience member":              func(c jwt.MapClaims, _ map[string]any) { c["aud"] = []string{"client", ""} },
		"no expiry":                            func(c jwt.MapClaims, _ map[string]any) { delete(c, "exp") },
		"expired":                              func(c jwt.MapClaims, _ map[string]any) { c["exp"] = time.Now().Add(-time.Second).Unix() },
		"not active":                           func(c jwt.MapClaims, _ map[string]any) { c["nbf"] = time.Now().Add(time.Hour).Unix() },
		"no kid":                               func(_ jwt.MapClaims, h map[string]any) { delete(h, "kid") },
		"invalid kid":                          func(_ jwt.MapClaims, h map[string]any) { h["kid"] = 12 },
		"refresh must not fall back":           func(c jwt.MapClaims, _ map[string]any) { c["token_use"] = "refresh" },
		"incomplete native must not fall back": func(c jwt.MapClaims, _ map[string]any) { c["token_use"] = "access" },
		"native cluster must not fall back":    func(c jwt.MapClaims, _ map[string]any) { c["cluster_id"] = "cluster-a" },
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := CheckDelegation(sign(change), "cluster-a", "node-b"); !errors.Is(err, ErrInvalidToken) {
				t.Fatalf("got %v", err)
			}
		})
	}
	if _, err := CheckDelegation(sign(func(c jwt.MapClaims, _ map[string]any) { c["aud"] = []string{"client-a", "client-b"} }), "cluster-a", "node-b"); err != nil {
		t.Fatal("valid audience array rejected")
	}
	for _, method := range []jwt.SigningMethod{jwt.SigningMethodHS256, jwt.SigningMethodNone} {
		var signingKey any = []byte("not-a-public-key")
		if method == jwt.SigningMethodNone {
			signingKey = jwt.UnsafeAllowNoneSignatureType
		}
		token := jwt.NewWithClaims(method, jwt.MapClaims{"iss": "https://idp.example.test/", "sub": "opaque-subject", "aud": "client", "exp": time.Now().Add(time.Hour).Unix()})
		token.Header["kid"] = "key"
		raw, err := token.SignedString(signingKey)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := CheckDelegation(raw, "cluster-a", "node-b"); !errors.Is(err, ErrInvalidToken) {
			t.Fatal("unsafe OpenID signing algorithm accepted")
		}
	}
}
