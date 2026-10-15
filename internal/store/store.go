// Package store owns every file aits writes (P7): one root, $AITS_HOME
// (default ~/.local/share/aits), with one directory per command group;
// directories 0700, files 0600; writes go through a temporary file and a
// rename, so a reader sees the old file or the new one, never half of one.
package store

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

// ErrNoRoot means the root cannot be found: neither AITS_HOME nor HOME is set.
var ErrNoRoot = errors.New("neither AITS_HOME nor HOME is set")

// Dir returns the directory of group under the root, creating it 0700 and
// tightening the root and the directory to 0700 if they exist.
func Dir(environ []string, group string) (string, error) {
	root := Lookup(environ, "AITS_HOME")
	if root == "" {
		home := Lookup(environ, "HOME")
		if home == "" {
			return "", ErrNoRoot
		}
		root = filepath.Join(home, ".local", "share", "aits")
	}
	dir := filepath.Join(root, group)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	for _, d := range []string{root, dir} {
		if err := os.Chmod(d, 0o700); err != nil {
			return "", err
		}
	}
	return dir, nil
}

// WriteFile replaces path with data through a temporary file in the same
// directory and a rename. The file is 0600.
func WriteFile(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err != nil {
		_ = os.Remove(f.Name())
	}
	return err
}

// Lock takes an exclusive lock on dir/.lock, waiting for it if another
// process holds it, and returns the function that releases it.
func Lock(dir string) (func(), error) {
	return lockFile(filepath.Join(dir, ".lock"), true, syscall.LOCK_EX)
}

// ErrLocked means another holder has the lock TryLock asked for.
var ErrLocked = errors.New("locked by another process")

// TryLock takes an exclusive lock on path without waiting and returns the
// function that releases it. With create it creates path, or tightens an
// existing file, to 0600; without it a missing path is an os.ErrNotExist
// error and nothing is created. Another holder gives ErrLocked.
func TryLock(path string, create bool) (func(), error) {
	return lockFile(path, create, syscall.LOCK_EX|syscall.LOCK_NB)
}

// lockFile opens the lock file path (see OpenRegular), creating it 0600 or
// tightening it to 0600 with create, and flocks it as how says.
func lockFile(path string, create bool, how int) (func(), error) {
	flag := os.O_RDWR
	if create {
		flag |= os.O_CREATE
	}
	f, fi, err := OpenRegular(path, flag, 0o600)
	if err != nil {
		return nil, err
	}
	if create && fi.Mode().Perm() != 0o600 {
		err = f.Chmod(0o600)
	}
	if err == nil {
		err = syscall.Flock(int(f.Fd()), how)
	}
	if err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) {
			err = ErrLocked
		}
		return nil, err
	}
	return func() { f.Close() }, nil // closing the file releases the lock
}

// OpenRegular opens path without following a symlink or blocking on a FIFO,
// and requires a regular file: a symlink would reach another file, and a
// FIFO or device is no aits file.
func OpenRegular(path string, flag int, perm os.FileMode) (*os.File, os.FileInfo, error) {
	f, err := os.OpenFile(path, flag|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, perm)
	if err != nil {
		return nil, nil, err
	}
	fi, err := f.Stat()
	if err == nil && !fi.Mode().IsRegular() {
		err = fmt.Errorf("%s: not a regular file", path)
	}
	if err != nil {
		f.Close()
		return nil, nil, err
	}
	return f, fi, nil
}

// Lookup returns the last value of name in environ, or "".
func Lookup(environ []string, name string) string {
	val := ""
	for _, kv := range environ {
		if k, v, ok := strings.Cut(kv, "="); ok && k == name {
			val = v
		}
	}
	return val
}
