package identity_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/atikansari-ghr/BookmarksSync/internal/identity"
	"github.com/atikansari-ghr/BookmarksSync/internal/webauthntest"
)

// authenticatorChange is one factor change performed from the acting session.
type authenticatorChange struct {
	name string
	// setup runs before the sessions under test exist (it may revoke sessions).
	setup func(t *testing.T, e *passkeyEnv) any
	// change performs the change from session sessionID.
	change func(t *testing.T, e *passkeyEnv, state any, sessionID int64) (identity.SessionRotation, error)
	// applied reports whether the change took effect (for the atomicity check).
	applied func(t *testing.T, e *passkeyEnv, state any) bool
}

func authenticatorChanges() []authenticatorChange {
	ctx := context.Background()
	totpEnabled := func(t *testing.T, e *passkeyEnv) bool {
		st, err := e.svc.TOTPStatus(ctx, e.alice.ID)
		if err != nil {
			t.Fatal(err)
		}
		return st.Enabled
	}
	return []authenticatorChange{
		{
			name: "confirm TOTP",
			setup: func(t *testing.T, e *passkeyEnv) any {
				enr, err := e.svc.BeginTOTPEnrollment(ctx, e.alice.ID)
				if err != nil {
					t.Fatal(err)
				}
				return enr.Secret
			},
			change: func(t *testing.T, e *passkeyEnv, s any, sid int64) (identity.SessionRotation, error) {
				_, rot, err := e.svc.ConfirmTOTPEnrollment(ctx, e.alice.ID, sid, testTOTP(t, s.(string), e.clock.Now()))
				return rot, err
			},
			applied: func(t *testing.T, e *passkeyEnv, _ any) bool { return totpEnabled(t, e) },
		},
		{
			name:  "disable TOTP",
			setup: func(t *testing.T, e *passkeyEnv) any { e.enrollTOTP(t, e.alice); return nil },
			change: func(t *testing.T, e *passkeyEnv, _ any, sid int64) (identity.SessionRotation, error) {
				return e.svc.DisableTOTP(ctx, e.alice.ID, sid)
			},
			applied: func(t *testing.T, e *passkeyEnv, _ any) bool { return !totpEnabled(t, e) },
		},
		{
			name: "regenerate recovery codes",
			setup: func(t *testing.T, e *passkeyEnv) any {
				_, codes := e.enrollTOTP(t, e.alice)
				return codes
			},
			change: func(t *testing.T, e *passkeyEnv, _ any, sid int64) (identity.SessionRotation, error) {
				_, rot, err := e.svc.RegenerateRecoveryCodes(ctx, e.alice.ID, sid)
				return rot, err
			},
			applied: func(t *testing.T, e *passkeyEnv, s any) bool {
				// The old codes stop working once regenerated. Probe with the last one.
				codes := s.([]string)
				return e.svc.ConsumeRecoveryCode(ctx, e.alice.ID, codes[len(codes)-1]) != nil
			},
		},
		{
			name:  "register passkey",
			setup: func(t *testing.T, e *passkeyEnv) any { return webauthntest.New(rpOrigin) },
			change: func(t *testing.T, e *passkeyEnv, s any, sid int64) (identity.SessionRotation, error) {
				c, err := e.svc.BeginPasskeyRegistration(ctx, e.alice.ID, sid)
				if err != nil {
					// A dead session may already fail here; report it like the finish step.
					return identity.SessionRotation{}, identity.ErrUnauthorized
				}
				resp, _, err := s.(*webauthntest.Authenticator).Register(c.Options, webauthntest.Overrides{})
				if err != nil {
					t.Fatal(err)
				}
				_, rot, err := e.svc.FinishPasskeyRegistration(ctx, e.alice.ID, sid, c.FlowID, "Key", resp)
				return rot, err
			},
			applied: func(t *testing.T, e *passkeyEnv, _ any) bool {
				list, _ := e.svc.ListPasskeys(ctx, e.alice.ID)
				return len(list) == 1
			},
		},
		{
			name: "delete passkey",
			setup: func(t *testing.T, e *passkeyEnv) any {
				pk, _ := e.register(t, webauthntest.New(rpOrigin), e.alice, e.aliceSess)
				return pk.ID
			},
			change: func(t *testing.T, e *passkeyEnv, s any, sid int64) (identity.SessionRotation, error) {
				return e.svc.DeletePasskey(ctx, e.alice.ID, s.(int64), sid)
			},
			applied: func(t *testing.T, e *passkeyEnv, _ any) bool {
				list, _ := e.svc.ListPasskeys(ctx, e.alice.ID)
				return len(list) == 0
			},
		},
	}
}

func TestAuthenticatorChangesRevokeOtherSessionsAndRotateTheActingOne(t *testing.T) {
	for _, tc := range authenticatorChanges() {
		t.Run(tc.name, func(t *testing.T) {
			e := newPasskeyEnv(t)
			ctx := context.Background()
			state := tc.setup(t, e)
			acting, actingSecret, err := e.svc.CreateSession(ctx, e.alice.ID, "acting")
			if err != nil {
				t.Fatal(err)
			}
			_, otherSecret, _ := e.svc.CreateSession(ctx, e.alice.ID, "other")
			_, bobSecret, _ := e.svc.CreateSession(ctx, e.bob.ID, "bob")
			e.clock.Advance(2 * time.Minute) // makes an authenticated_at change visible

			rot, err := tc.change(t, e, state, acting.ID)
			if err != nil {
				t.Fatalf("change: %v", err)
			}
			if !tc.applied(t, e, state) {
				t.Fatal("change not applied")
			}
			if rot.Secret == "" || rot.Secret == actingSecret || rot.Session.ID != acting.ID {
				t.Fatalf("rotation = id %d, fresh secret %v", rot.Session.ID, rot.Secret != "" && rot.Secret != actingSecret)
			}
			if _, _, err := e.svc.ResolveSession(ctx, actingSecret); !errors.Is(err, identity.ErrUnauthorized) {
				t.Fatalf("pre-change acting secret still works: %v", err)
			}
			got, _, err := e.svc.ResolveSession(ctx, rot.Secret)
			if err != nil || got.ID != acting.ID {
				t.Fatalf("rotated secret: %+v, %v", got, err)
			}
			// Rotation is not a new authentication: recent-auth age and absolute expiry stay.
			if !got.AuthenticatedAt.Equal(acting.AuthenticatedAt) || !got.ExpiresAt.Equal(acting.ExpiresAt) ||
				!rot.Session.AuthenticatedAt.Equal(acting.AuthenticatedAt) || !rot.Session.ExpiresAt.Equal(acting.ExpiresAt) {
				t.Fatalf("rotation changed authenticated_at/expires_at: %+v vs %+v", got, acting)
			}
			if _, _, err := e.svc.ResolveSession(ctx, otherSecret); !errors.Is(err, identity.ErrUnauthorized) {
				t.Fatalf("other session survived: %v", err)
			}
			if _, _, err := e.svc.ResolveSession(ctx, bobSecret); err != nil {
				t.Fatalf("another user's session was affected: %v", err)
			}
		})
	}
}

// If the acting session is no longer live (or is another user's), nothing changes:
// the change, the revocation and the rotation are one transaction.
func TestAuthenticatorChangesFromADeadSessionChangeNothing(t *testing.T) {
	for _, tc := range authenticatorChanges() {
		t.Run(tc.name, func(t *testing.T) {
			e := newPasskeyEnv(t)
			ctx := context.Background()
			state := tc.setup(t, e)
			acting, _, _ := e.svc.CreateSession(ctx, e.alice.ID, "acting")
			_, otherSecret, _ := e.svc.CreateSession(ctx, e.alice.ID, "other")
			if err := e.svc.RevokeSession(ctx, e.alice.ID, acting.ID); err != nil {
				t.Fatal(err)
			}
			if _, err := tc.change(t, e, state, acting.ID); !errors.Is(err, identity.ErrUnauthorized) {
				t.Fatalf("change from a revoked session: %v", err)
			}
			if tc.applied(t, e, state) {
				t.Fatal("change applied from a revoked session")
			}
			if _, _, err := e.svc.ResolveSession(ctx, otherSecret); err != nil {
				t.Fatalf("other session revoked by a refused change: %v", err)
			}
			bobs, _, _ := e.svc.CreateSession(ctx, e.bob.ID, "bob")
			if _, err := tc.change(t, e, state, bobs.ID); err == nil {
				t.Fatal("change accepted from another user's session")
			}
		})
	}
}
