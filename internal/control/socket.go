package control

import (
	"context"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

func Listen(path string) (net.Listener, error) {
	if !filepath.IsAbs(path) {
		return nil, errors.New("absolute Unix socket path required")
	}
	dir := filepath.Dir(path)
	for p := dir; ; p = filepath.Dir(p) {
		fi, err := os.Lstat(p)
		if err == nil {
			if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
				return nil, errors.New("socket ancestors must be real directories")
			}
		} else if !os.IsNotExist(err) {
			return nil, err
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	fi, err := os.Stat(dir)
	if err != nil || fi.Mode().Perm() != 0700 {
		return nil, errors.New("socket directory must be mode 0700")
	}
	// Never remove a stale socket automatically: another owner may still use it.
	if _, err = os.Lstat(path); !os.IsNotExist(err) {
		return nil, errors.New("socket path already exists or is inaccessible")
	}
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	if err = os.Chmod(path, 0600); err != nil {
		l.Close()
		return nil, err
	}
	return l, nil
}
func Serve(ctx context.Context, l net.Listener, s *Service) error {
	server := &http.Server{Handler: s, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 15 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 8192}
	done := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = server.Close()
		case <-done:
		}
	}()
	err := server.Serve(l)
	close(done)
	s.Close()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
func Client(path string) *http.Client {
	return &http.Client{Timeout: 20 * time.Second, Transport: &http.Transport{DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}}
}
