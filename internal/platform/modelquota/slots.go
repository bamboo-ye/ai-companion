// Package modelquota bounds requests per provider across Go/Python processes
// sharing a private local directory. Locks are reclaimed on process death.
package modelquota

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type Transport struct{ Base http.RoundTripper }

func (t Transport) RoundTrip(req *http.Request) (*http.Response, error) {
	release, err := Acquire(req.Context(), strings.ToLower(req.URL.Host))
	if err != nil {
		return nil, err
	}
	base := t.Base
	if base == nil {
		base = http.DefaultTransport
	}
	res, err := base.RoundTrip(req)
	if err != nil {
		release()
		return nil, err
	}
	res.Body = &permitBody{ReadCloser: res.Body, release: release}
	return res, nil
}

type permitBody struct {
	io.ReadCloser
	once    sync.Once
	release func()
}

func (b *permitBody) Close() error { defer b.once.Do(b.release); return b.ReadCloser.Close() }
func (b *permitBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil {
		b.once.Do(b.release)
	}
	return n, err
}

func Acquire(ctx context.Context, host string) (func(), error) {
	limit := 8
	if raw := os.Getenv("MODEL_PROVIDER_CONCURRENCY"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > 64 {
			return nil, fmt.Errorf("MODEL_PROVIDER_CONCURRENCY must be between 1 and 64")
		}
		limit = n
	}
	root := os.Getenv("MODEL_CONCURRENCY_DIR")
	if root == "" {
		root = filepath.Join(os.TempDir(), fmt.Sprintf("ai-companion-model-slots-%d", os.Getuid()))
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(root)
	if err != nil {
		return nil, err
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	shared := false
	if raw := os.Getenv("MODEL_CONCURRENCY_SHARED_GID"); raw != "" && ok {
		gid, err := strconv.Atoi(raw)
		if err != nil {
			return nil, fmt.Errorf("invalid shared model concurrency gid")
		}
		groups, _ := os.Getgroups()
		groups = append(groups, os.Getgid())
		for _, g := range groups {
			if g == gid && int(owner.Gid) == gid && info.Mode().Perm()&0007 == 0 && info.Mode()&os.ModeSetgid != 0 {
				shared = true
			}
		}
	}
	private := ok && int(owner.Uid) == os.Getuid() && info.Mode().Perm()&0077 == 0
	if !info.IsDir() || (!private && !shared) {
		return nil, fmt.Errorf("model concurrency directory must be private or use the configured shared group")
	}
	key := fmt.Sprintf("%x", sha256.Sum256([]byte(host)))
	for {
		for i := 0; i < limit; i++ {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			mode := uint32(0600)
			if shared {
				mode = 0640
			}
			fd, err := syscall.Open(filepath.Join(root, fmt.Sprintf("%s-%d.lock", key, i)), syscall.O_CREAT|syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, mode)
			if err == syscall.EACCES && shared {
				continue
			}
			if err != nil {
				return nil, err
			}
			if shared {
				var st syscall.Stat_t
				if syscall.Fstat(fd, &st) == nil && int(st.Uid) == os.Getuid() {
					if err := syscall.Fchmod(fd, 0640); err != nil {
						_ = syscall.Close(fd)
						return nil, err
					}
				}
			}
			err = syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB)
			if err == nil {
				var once sync.Once
				return func() { once.Do(func() { _ = syscall.Flock(fd, syscall.LOCK_UN); _ = syscall.Close(fd) }) }, nil
			}
			_ = syscall.Close(fd)
			if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
				return nil, err
			}
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}
