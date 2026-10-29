// Package pairing stores local device credentials without exposing their contents.
package pairing

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

var errProtected = errors.New("pairing storage must be a protected regular file in a private directory")

func directory(path string, create bool) error {
	if !filepath.IsAbs(path) {
		return errProtected
	}
	dir := filepath.Dir(path)
	for p := dir; ; p = filepath.Dir(p) {
		fi, err := os.Lstat(p)
		if err == nil {
			if !fi.IsDir() || fi.Mode()&os.ModeSymlink != 0 {
				return errProtected
			}
		} else if !os.IsNotExist(err) || !create {
			return errProtected
		}
		if p == filepath.Dir(p) {
			break
		}
	}
	if create {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return errProtected
		}
	}
	fi, err := os.Stat(dir)
	if err != nil || fi.Mode().Perm() != 0700 {
		return errProtected
	}
	return nil
}
func open(path string) (*os.File, error) {
	fd, err := syscall.Open(path, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, errProtected
	}
	f := os.NewFile(uintptr(fd), "pairing")
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() || fi.Mode().Perm() != 0600 || fi.Size() > 16384 {
		f.Close()
		return nil, errProtected
	}
	return f, nil
}
func Load(path string, value any) error {
	if err := directory(path, false); err != nil {
		return err
	}
	f, err := open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 16385))
	if d.Decode(value) != nil {
		return errors.New("invalid pairing file")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errors.New("invalid pairing file")
	}
	return nil
}
func Save(path string, value any) error {
	if err := directory(path, true); err != nil {
		return err
	}
	if _, err := os.Lstat(path); err == nil {
		f, e := open(path)
		if e != nil {
			return e
		}
		f.Close()
	} else if !os.IsNotExist(err) {
		return errProtected
	}
	raw, err := json.Marshal(value)
	if err != nil || len(raw) > 16384 {
		return errors.New("invalid pairing data")
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".pairing-*")
	if err != nil {
		return errProtected
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(raw); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return errProtected
	}
	if os.Rename(f.Name(), path) != nil {
		return errProtected
	}
	return nil
}
