package auth

import (
	"context"
	"encoding/base64"
	"fmt"
	"testing"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
)

type timeoutKeySet struct{}
type keyNetworkTimeout struct{}

func (keyNetworkTimeout) Error() string   { return "private network failure" }
func (keyNetworkTimeout) Timeout() bool   { return true }
func (keyNetworkTimeout) Temporary() bool { return true }
func (timeoutKeySet) VerifySignature(context.Context, string) ([]byte, error) {
	return nil, keyNetworkTimeout{}
}

func TestOIDCKeyVerificationPreservesTransportFailureBeforeContextExpires(t *testing.T) {
	now := time.Now()
	encode := func(s string) string { return base64.RawURLEncoding.EncodeToString([]byte(s)) }
	token := encode(`{"alg":"RS256"}`) + "." + encode(fmt.Sprintf(`{"iss":"https://issuer.example","aud":"app","sub":"user","exp":%d}`, now.Add(time.Hour).Unix())) + "." + encode("signature")
	failure := &signatureFailure{}
	ctx := context.WithValue(context.Background(), signatureFailureKey{}, failure)
	verifier := oidc.NewVerifier("https://issuer.example", preservingKeySet{KeySet: timeoutKeySet{}}, &oidc.Config{ClientID: "app"})
	_, err := verifier.Verify(ctx, token)
	if err == nil || ctx.Err() != nil {
		t.Fatalf("expected key failure before context deadline: %v", err)
	}
	// go-oidc v3.15.0 formats signature errors with %%v, losing the transport
	// error's type. Our per-verification capture must survive that formatting.
	if unavailable(ctx, err) {
		t.Fatal("test no longer reproduces upstream error wrapping")
	}
	if !unavailable(ctx, failure.err) {
		t.Fatalf("network failure lost: %v", failure.err)
	}
}
