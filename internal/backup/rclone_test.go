package backup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

type rcloneCall struct {
	program string
	args    []string
}

type fakeRcloneRunner struct {
	calls      []rcloneCall
	version    string
	failAt     string
	failAtCall int
	failErr    error
	lsfOutput  []byte
}

func commandArgs(call rcloneCall) []string {
	args := call.args
	if len(args) >= 2 && args[0] == "--config" {
		args = args[2:]
	}
	return args
}

func (f *fakeRcloneRunner) Run(_ context.Context, program string, args []string) ([]byte, error) {
	f.calls = append(f.calls, rcloneCall{program: program, args: append([]string(nil), args...)})
	commandIndex := 0
	if len(args) >= 3 && args[0] == "--config" {
		commandIndex = 2
	}
	command := args[commandIndex]
	if command == f.failAt {
		return []byte("secret=do-not-expose"), f.failErr
	}
	if f.failAtCall > 0 && len(f.calls) == f.failAtCall {
		return []byte("secret=do-not-expose"), errors.New("injected command failure")
	}
	if command == "version" {
		return []byte(f.version), nil
	}
	if command == "lsf" {
		return append([]byte(nil), f.lsfOutput...), nil
	}
	return nil, nil
}

func TestRcloneDoesNotReplaceOrDeleteAnExistingGeneratedArchive(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source.zip")
	if err := os.WriteFile(source, []byte("archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRcloneRunner{version: "rclone v1.76.0", lsfOutput: []byte("already-there.zip\n")}
	driver := RcloneDriver{BinaryPath: "rclone", Runner: runner}
	name := backupArchiveName(TriggerManual, time.Now(), strings.Repeat("e", 32))
	if err := driver.Publish(context.Background(), Destination{Kind: DestinationRclone, Reference: "drive:backups"}, source, name); err == nil {
		t.Fatal("existing final archive was accepted")
	}
	for _, call := range runner.calls {
		args := commandArgs(call)
		if args[0] == "copyto" && len(args) > 3 && args[3] == remotePath("drive:backups", name) {
			t.Fatal("existing final object was replaced")
		}
		if args[0] == "deletefile" && len(args) > 1 && args[1] == remotePath("drive:backups", name) {
			t.Fatal("existing final object was deleted")
		}
	}
}

func TestRcloneRemovesUnverifiedFinalObjects(t *testing.T) {
	for _, tc := range []struct {
		name     string
		failCall int
	}{{"final-copy-failure", 7}, {"final-check-failure", 10}} {
		t.Run(tc.name, func(t *testing.T) {
			source := filepath.Join(t.TempDir(), "source.zip")
			if err := os.WriteFile(source, []byte("archive"), 0o600); err != nil {
				t.Fatal(err)
			}
			runner := &fakeRcloneRunner{version: "rclone v1.76.0", failAtCall: tc.failCall}
			driver := RcloneDriver{BinaryPath: "rclone", Runner: runner}
			name := backupArchiveName(TriggerManual, time.Now(), strings.Repeat("f", 32))
			dest := Destination{Kind: DestinationRclone, Reference: "drive:backups"}
			if err := driver.Publish(context.Background(), dest, source, name); err == nil {
				t.Fatal("injected publication failure was ignored")
			}
			foundFinalCleanup := false
			for _, call := range runner.calls {
				args := commandArgs(call)
				if args[0] == "deletefile" && len(args) > 1 && args[1] == remotePath(dest.Reference, name) {
					foundFinalCleanup = true
				}
			}
			if !foundFinalCleanup {
				t.Fatalf("unverified final object not cleaned: %#v", runner.calls)
			}
		})
	}
}

func TestRclonePublishUsesPinnedVersionAndChecksFinalBytes(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source.zip")
	if err := os.WriteFile(source, []byte("sensitive archive bytes"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRcloneRunner{version: "rclone v1.76.0\n- os/version: fake"}
	driver := RcloneDriver{BinaryPath: "C:/Program Files/rclone.exe", ConfigPath: "C:/service config/rclone.conf", Runner: runner}
	destination := Destination{Kind: DestinationRclone, Reference: "drive:Backup Folder; literal"}
	name := backupArchiveName(TriggerWeekly, time.Date(2026, 11, 22, 3, 0, 0, 0, time.UTC), strings.Repeat("a", 32))
	if err := driver.Publish(context.Background(), destination, source, name); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 10 {
		t.Fatalf("rclone commands = %d, want version/mkdir/mkdir/copy/check/lsf/copy/delete/rmdir/check; %#v", len(runner.calls), runner.calls)
	}
	commands := []string{"version", "mkdir", "mkdir", "copyto", "check", "lsf", "copyto", "deletefile", "rmdir", "check"}
	for i, want := range commands {
		if commandArgs(runner.calls[i])[0] != want {
			t.Fatalf("command %d = %#v, want %s", i, runner.calls[i].args, want)
		}
		if runner.calls[i].program != driver.BinaryPath || runner.calls[i].args[0] != "--config" || runner.calls[i].args[1] != driver.ConfigPath {
			t.Fatalf("command %d did not use fixed executable/config: %#v", i, runner.calls[i])
		}
	}
	if commandArgs(runner.calls[3])[2] != source || commandArgs(runner.calls[3])[3] == remotePath(destination.Reference, name) {
		t.Fatalf("upload should target a temporary remote name: %#v", runner.calls[3].args)
	}
	if !reflect.DeepEqual(commandArgs(runner.calls[4])[:1], []string{"check"}) || !strings.Contains(strings.Join(runner.calls[4].args, " "), "--download") {
		t.Fatalf("temporary archive was not byte-verified: %#v", runner.calls[4].args)
	}
	if commandArgs(runner.calls[6])[3] != remotePath(destination.Reference, name) || !strings.Contains(strings.Join(runner.calls[6].args, " "), "--immutable") {
		t.Fatalf("final object was not published without replacement: %#v", runner.calls[6].args)
	}
	if !strings.Contains(strings.Join(runner.calls[9].args, " "), destination.Reference) || !strings.Contains(strings.Join(runner.calls[9].args, " "), name) {
		t.Fatalf("final archive was not verified: %#v", runner.calls[9].args)
	}
}

func TestRcloneRejectsWrongVersionBeforeDestinationWork(t *testing.T) {
	runner := &fakeRcloneRunner{version: "rclone v1.75.2"}
	driver := RcloneDriver{BinaryPath: "rclone", Runner: runner}
	err := driver.Test(context.Background(), Destination{Kind: DestinationRclone, Reference: "drive:backups"})
	if err == nil || strings.Contains(err.Error(), "1.75.2") || len(runner.calls) != 1 {
		t.Fatalf("wrong rclone version result=%v, calls=%#v", err, runner.calls)
	}
}

func TestRcloneMasksProcessErrorsAndDoesNotContinueAfterFailedCheck(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source.zip")
	if err := os.WriteFile(source, []byte("contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRcloneRunner{version: "rclone v1.76.0", failAt: "check", failErr: errors.New("token=hidden")}
	driver := RcloneDriver{BinaryPath: "rclone", Runner: runner}
	destination := Destination{Kind: DestinationRclone, Reference: "drive:backups"}
	name := backupArchiveName(TriggerManual, time.Now(), strings.Repeat("b", 32))
	err := driver.Publish(context.Background(), destination, source, name)
	if err == nil || strings.Contains(err.Error(), "hidden") || strings.Contains(err.Error(), "secret") {
		t.Fatalf("raw command output leaked in error: %v", err)
	}
	if len(runner.calls) != 7 || commandArgs(runner.calls[5])[0] != "deletefile" || commandArgs(runner.calls[6])[0] != "rmdir" {
		t.Fatalf("unexpected work after verification failure (only temporary cleanup is allowed): %#v", runner.calls)
	}
}

func TestRclonePublishCleansPartialUploadWhenCopyFails(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source.zip")
	if err := os.WriteFile(source, []byte("archive"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRcloneRunner{version: "rclone v1.76.0", failAt: "copyto", failErr: errors.New("interrupted upload")}
	driver := RcloneDriver{BinaryPath: "rclone", Runner: runner}
	name := backupArchiveName(TriggerManual, time.Now(), strings.Repeat("d", 32))
	if err := driver.Publish(context.Background(), Destination{Kind: DestinationRclone, Reference: "drive:backups"}, source, name); err == nil {
		t.Fatal("interrupted upload succeeded")
	}
	if len(runner.calls) != 6 || commandArgs(runner.calls[4])[0] != "deletefile" || commandArgs(runner.calls[5])[0] != "rmdir" {
		t.Fatalf("partial upload cleanup calls=%#v", runner.calls)
	}
}

func TestRcloneDestinationProbeCleansPartialUploadWhenCopyFails(t *testing.T) {
	runner := &fakeRcloneRunner{version: "rclone v1.76.0", failAt: "copyto", failErr: errors.New("interrupted probe")}
	driver := RcloneDriver{BinaryPath: "rclone", Runner: runner}
	if err := driver.Test(context.Background(), Destination{Kind: DestinationRclone, Reference: "drive:backups"}); err == nil {
		t.Fatal("interrupted probe succeeded")
	}
	if len(runner.calls) != 6 || commandArgs(runner.calls[4])[0] != "deletefile" || commandArgs(runner.calls[5])[0] != "rmdir" {
		t.Fatalf("partial probe cleanup calls=%#v", runner.calls)
	}
}

func TestRcloneUsesArgumentArrayForHostileLookingRemotePath(t *testing.T) {
	source := filepath.Join(t.TempDir(), "source.zip")
	if err := os.WriteFile(source, []byte("contents"), 0o600); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRcloneRunner{version: "rclone v1.76.0"}
	driver := RcloneDriver{BinaryPath: "rclone", Runner: runner}
	destination := Destination{Kind: DestinationRclone, Reference: "remote:folder with spaces;not-a-shell-command"}
	name := backupArchiveName(TriggerManual, time.Now(), strings.Repeat("c", 32))
	if err := driver.Publish(context.Background(), destination, source, name); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(commandArgs(runner.calls[3])[2:3], []string{source}) {
		t.Fatalf("local path was not preserved as one process argument: %#v", runner.calls[3].args)
	}
	if !strings.Contains(commandArgs(runner.calls[3])[3], ";not-a-shell-command") {
		t.Fatalf("remote path was not passed literally as one process argument: %#v", runner.calls[3].args)
	}
}

func TestRcloneDeleteAcceptsOnlyGeneratedArchiveNames(t *testing.T) {
	runner := &fakeRcloneRunner{version: "rclone v1.76.0"}
	driver := RcloneDriver{BinaryPath: "rclone", Runner: runner}
	destination := Destination{Kind: DestinationRclone, Reference: "drive:backups"}
	if err := driver.Delete(context.Background(), destination, "../secret"); err == nil {
		t.Fatal("Delete accepted an unsafe name")
	}
	if len(runner.calls) != 0 {
		t.Fatalf("unsafe name launched rclone: %#v", runner.calls)
	}
}

func TestRcloneTestVerifiesAndCleansPrivateProbeAndSupportsRemoteRoot(t *testing.T) {
	runner := &fakeRcloneRunner{version: "rclone v1.76.0"}
	driver := RcloneDriver{BinaryPath: "rclone", ConfigPath: "C:/host config/rclone.conf", Runner: runner}
	if err := driver.Test(context.Background(), Destination{Kind: DestinationRclone, Reference: "nas:"}); err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 7 {
		t.Fatalf("destination test commands = %#v", runner.calls)
	}
	args := commandArgs(runner.calls[3])
	if args[0] != "copyto" || args[1] != "--immutable" || !strings.HasPrefix(args[3], "nas:.bookmarksync-probe-") || !strings.HasSuffix(args[3], "/probe.bin") {
		t.Fatalf("probe copy command = %#v", args)
	}
	if runner.calls[0].args[0] != "--config" || runner.calls[0].args[1] != driver.ConfigPath {
		t.Fatalf("fixed host config not used: %#v", runner.calls[0])
	}
	if commandArgs(runner.calls[5])[0] != "deletefile" || commandArgs(runner.calls[6])[0] != "rmdir" {
		t.Fatalf("probe cleanup was not attempted: %#v", runner.calls[5:])
	}
}
