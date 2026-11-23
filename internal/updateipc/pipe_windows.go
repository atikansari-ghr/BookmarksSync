//go:build windows

package updateipc

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

const PipeName = `\\.\pipe\BookmarksSyncUpdater`

// PipeHandler receives one client connection. The listener serializes clients
// so only one updater request can be active at a time.
type PipeHandler func(context.Context, io.ReadWriteCloser)

func ListenAndServe(ctx context.Context, serviceSID string, handler PipeHandler) error {
	if handler == nil {
		return errors.New("updater pipe handler is required")
	}
	security, err := pipeSecurity(serviceSID)
	if err != nil {
		return err
	}
	for ctx.Err() == nil {
		pipe, err := createPipe(security)
		if err != nil {
			return errors.New("could not create updater pipe")
		}
		file := os.NewFile(uintptr(pipe), "BookmarksSyncUpdater")
		connected := make(chan error, 1)
		go func() { connected <- windows.ConnectNamedPipe(pipe, nil) }()
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil
		case err = <-connected:
			if errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
				err = nil
			}
			if err != nil {
				_ = file.Close()
				if ctx.Err() != nil {
					return nil
				}
				return errors.New("updater pipe connection failed")
			}
		}
		requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
		handled := make(chan struct{})
		go func() {
			handler(requestCtx, file)
			close(handled)
		}()
		select {
		case <-handled:
		case <-requestCtx.Done():
			_ = file.Close()
			<-handled
		}
		cancel()
		_ = file.Close()
	}
	return nil
}

func createPipe(sa *windows.SecurityAttributes) (windows.Handle, error) {
	name, _ := windows.UTF16PtrFromString(PipeName)
	return windows.CreateNamedPipe(name,
		pipeOpenMode(), pipeMode(),
		1, maxFrameBytes+2, maxFrameBytes+2, 0, sa)
}

func pipeOpenMode() uint32 { return windows.PIPE_ACCESS_DUPLEX | windows.FILE_FLAG_FIRST_PIPE_INSTANCE }
func pipeMode() uint32 {
	return windows.PIPE_TYPE_BYTE | windows.PIPE_READMODE_BYTE | windows.PIPE_WAIT | windows.PIPE_REJECT_REMOTE_CLIENTS
}

func pipeSecurity(serviceSID string) (*windows.SecurityAttributes, error) {
	return pipeSecurityFor(serviceSID, true)
}

func pipeSecurityFor(serviceSID string, allowAdministrators bool) (*windows.SecurityAttributes, error) {
	sid, err := windows.StringToSid(serviceSID)
	if err != nil {
		return nil, errors.New("invalid updater service SID")
	}
	admins := ""
	if allowAdministrators {
		admins = "(A;;GA;;;BA)"
	}
	sddl := fmt.Sprintf("D:P(A;;GA;;;SY)%s(A;;0x0012019f;;;%s)", admins, sid.String())
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return nil, errors.New("could not configure updater pipe permissions")
	}
	return &windows.SecurityAttributes{Length: uint32(unsafeSizeofSecurityAttributes()), SecurityDescriptor: sd}, nil
}

func DialPipe(ctx context.Context) (io.ReadWriteCloser, error) {
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		name, _ := windows.UTF16PtrFromString(PipeName)
		h, err := windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, 0, 0)
		if err == nil {
			return os.NewFile(uintptr(h), "BookmarksSyncUpdater-client"), nil
		}
		if !errors.Is(err, windows.ERROR_PIPE_BUSY) && !errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			return nil, fmt.Errorf("updater service is unavailable: %w", err)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, errors.New("updater service is unavailable")
		case <-tick.C:
		}
	}
}

// These indirections keep the Windows-only unsafe operations out of protocol.go.
func unsafePointer(p *windows.SECURITY_DESCRIPTOR) uintptr { return uintptr(unsafe.Pointer(p)) }
func unsafeSizeofSecurityAttributes() uintptr              { return unsafe.Sizeof(windows.SecurityAttributes{}) }
