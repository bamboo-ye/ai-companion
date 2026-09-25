package modelquota

import (
	"bufio"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func quotaFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MODEL_CONCURRENCY_DIR", dir)
	t.Setenv("MODEL_CONCURRENCY_SHARED_GID", "")
	t.Setenv("MODEL_PROVIDER_CONCURRENCY", "1")
	return dir
}

func TestProviderSlotsRespectDeadlineReleaseAndProviderScope(t *testing.T) {
	quotaFixture(t)
	release, err := Acquire(context.Background(), "one")
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if _, err := Acquire(ctx, "one"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	other, err := Acquire(context.Background(), "two")
	if err != nil {
		t.Fatal(err)
	}
	other()
	release()
	release() // Idempotent release must not free another caller's permit.
	again, err := Acquire(context.Background(), "one")
	if err != nil {
		t.Fatal(err)
	}
	again()
}

func TestProviderSlotsInteroperateWithPythonAndReleaseAfterCrash(t *testing.T) {
	dir := quotaFixture(t)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("Python is required for cross-language lock test")
	}
	path := filepath.Join(dir, fmt.Sprintf("%x-0.lock", sha256.Sum256([]byte("provider.test"))))
	cmd := exec.Command(python, "-c", `import fcntl,os,sys,time
fd=os.open(sys.argv[1],os.O_CREAT|os.O_RDONLY,0o600)
fcntl.flock(fd,fcntl.LOCK_EX)
print("ready",flush=True)
sys.stdin.read()
`, path)
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	in, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = cmd.Process.Kill(); _ = cmd.Wait() }()
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil || line != "ready\n" {
		t.Fatalf("child %q %v", line, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	if _, err := Acquire(ctx, "provider.test"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Python lock was ignored: %v", err)
	}
	_ = cmd.Process.Kill()
	_ = cmd.Wait()
	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	release, err := Acquire(ctx2, "provider.test")
	if err != nil {
		t.Fatalf("crash leaked permit: %v", err)
	}
	release()
}
