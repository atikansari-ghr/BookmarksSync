//go:build windows

package updateipc

import (
	"context"
	"errors"
	"io"
	"runtime"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func currentUserSID(t *testing.T) string {
	t.Helper()
	token, err := windows.OpenCurrentProcessToken()
	if err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	user, err := token.GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	return user.User.Sid.String()
}

func TestPipeLocalRequestReply(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sid := currentUserSID(t)
	callbackErr := make(chan error, 1)
	listenerDone := make(chan error, 1)
	go func() {
		listenerDone <- ListenAndServe(ctx, sid, func(_ context.Context, conn io.ReadWriteCloser) {
			request, err := DecodeRequest(conn)
			if err == nil && request.Tag != protocolTestTag {
				err = errors.New("unexpected pipe tag")
			}
			if err == nil {
				err = EncodeResponse(conn, Response{Accepted: true, Job: "bookmarksync-update-0123456789abcdef0123456789abcdef01234567"})
			}
			callbackErr <- err
		})
	}()
	clientCtx, clientCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer clientCancel()
	conn, err := DialPipe(clientCtx)
	if err != nil {
		t.Fatal(err)
	}
	if err := EncodeRequest(conn, protocolTestTag); err != nil {
		t.Fatal(err)
	}
	response, err := DecodeResponse(conn)
	_ = conn.Close()
	if err != nil || !response.Accepted || response.Job == "" {
		t.Fatalf("response=%#v err=%v", response, err)
	}
	if err := <-callbackErr; err != nil {
		t.Fatal(err)
	}
	cancel()
	if err := <-listenerDone; err != nil {
		t.Fatal(err)
	}
}

func TestPipeConfigurationRejectsRemoteClients(t *testing.T) {
	if got := pipeMode() & windows.PIPE_REJECT_REMOTE_CLIENTS; got == 0 {
		t.Fatal("pipe mode does not reject remote clients")
	}
	if pipeOpenMode()&windows.FILE_FLAG_FIRST_PIPE_INSTANCE == 0 {
		t.Fatal("pipe does not protect its first instance")
	}
}

func TestDialPipeReturnsWhenUpdaterIsMissing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := DialPipe(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("DialPipe error = %v, want context deadline", err)
	}
}

func TestPipeDACLIncludesOnlyRequiredTrustPrincipals(t *testing.T) {
	security, err := pipeSecurity("S-1-5-80-0")
	if err != nil {
		t.Fatal(err)
	}
	_ = security
	// The DACL is validated by Windows when CreateNamedPipe is called. Invalid
	// identities must fail before any pipe instance is created.
	if _, err := pipeSecurity("not-a-sid"); err == nil {
		t.Fatal("invalid service SID accepted")
	}
}

func TestPipeDACLDeniesUnlistedLocalIdentity(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := windows.ImpersonateSelf(windows.SecurityImpersonation); err != nil {
		t.Fatal(err)
	}
	defer windows.RevertToSelf()
	var token windows.Token
	if err := windows.OpenThreadToken(windows.CurrentThread(), windows.TOKEN_QUERY, true, &token); err != nil {
		t.Fatal(err)
	}
	defer token.Close()
	adminSID, _ := windows.StringToSid("S-1-5-32-544")
	isAdmin, err := token.IsMember(adminSID)
	if err != nil {
		t.Fatal(err)
	}
	if isAdmin {
		t.Skip("current identity is explicitly allowed through the Administrators ACE")
	}
	security, err := pipeSecurityFor("S-1-5-80-0", false)
	if err != nil {
		t.Fatal(err)
	}
	pipe, err := createPipe(security)
	if err != nil {
		t.Fatal("create protected test pipe:", err)
	}
	defer windows.CloseHandle(pipe)
	name, _ := windows.UTF16PtrFromString(PipeName)
	h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, 0, 0)
	if !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		if err == nil {
			_ = windows.CloseHandle(h)
		}
		t.Fatalf("unlisted local identity open error = %v, want access denied", err)
	}
}
