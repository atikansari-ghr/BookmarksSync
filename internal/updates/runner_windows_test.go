//go:build windows

package updates

import (
	"context"
	"testing"
)

func TestWindowsRunnerRejectsInvalidTagBeforeOpeningPipe(t *testing.T) {
	for _, tag := range []string{"", "latest", "build-ABCDEF0123456789ABCDEF0123456789ABCDEF01", "build-0123"} {
		if _, err := (windowsPipeRunner{}).Start(context.Background(), tag); err == nil {
			t.Errorf("Start(%q) succeeded; want invalid-tag error", tag)
		}
	}
}
