package auth

import (
	"context"

	"github.com/coreos/go-oidc/v3/oidc"
)

type signatureFailureKey struct{}
type signatureFailure struct{ err error }

// The wrapper remains shared with the cached verifier, while the error capture
// belongs to one call's context. RemoteKeySet's cache and inflight request
// coordination remain intact; concurrent login attempts cannot share captures.
type preservingKeySet struct{ oidc.KeySet }

func (keys preservingKeySet) VerifySignature(ctx context.Context, token string) ([]byte, error) {
	payload, err := keys.KeySet.VerifySignature(ctx, token)
	if failure, ok := ctx.Value(signatureFailureKey{}).(*signatureFailure); ok {
		failure.err = err
	}
	return payload, err
}
