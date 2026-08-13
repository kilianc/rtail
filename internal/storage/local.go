/*!
 * Local-disk implementation of storage.Backend.
 *
 * Object names are slash-separated and map onto a directory tree under root,
 * so the on-disk layout is the one in §3.4 and stays browsable with `ls`. That
 * is a feature, not an accident: handing someone a directory of Parquet files
 * they can open in DuckDB without rTail installed is the whole anti-lock-in
 * argument.
 */

package storage

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Local stores objects as files under a root directory.
type Local struct {
	root string
}

var _ Backend = (*Local)(nil)

// NewLocal opens (and creates, if missing) a local backend rooted at dir.
func NewLocal(dir string) (*Local, error) {
	absolute, err := filepath.Abs(dir)
	if nil != err {
		return nil, fmt.Errorf("resolving data dir: %w", err)
	}

	if err := os.MkdirAll(absolute, 0o755); nil != err {
		return nil, fmt.Errorf("creating data dir: %w", err)
	}

	return &Local{root: absolute}, nil
}

// Root is the absolute directory the backend writes into.
func (l *Local) Root() string { return l.root }

/*!
 * resolve maps an object name onto a path inside root.
 *
 * Object names reach us from the catalog, and one day from an API. Anything
 * that escapes the root is rejected rather than cleaned, so a name like
 * "../../etc/passwd" is an error and not a surprise.
 */
func (l *Local) resolve(name string) (string, error) {
	if "" == name || strings.HasPrefix(name, "/") {
		return "", fmt.Errorf("invalid object name %q", name)
	}

	path := filepath.Join(l.root, filepath.FromSlash(name))

	if path != l.root && !strings.HasPrefix(path, l.root+string(os.PathSeparator)) {
		return "", fmt.Errorf("object name %q escapes the data directory", name)
	}

	return path, nil
}

func (l *Local) Create(_ context.Context, name string) (Writer, error) {
	path, err := l.resolve(name)
	if nil != err {
		return nil, err
	}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); nil != err {
		return nil, fmt.Errorf("creating object directory: %w", err)
	}

	// The temp file lives in the destination directory so the rename is a
	// same-filesystem metadata operation, and therefore atomic.
	temp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".tmp-*")
	if nil != err {
		return nil, fmt.Errorf("creating temp object: %w", err)
	}

	return &localWriter{file: temp, final: path}, nil
}

func (l *Local) Open(_ context.Context, name string) (io.ReadSeekCloser, error) {
	path, err := l.resolve(name)
	if nil != err {
		return nil, err
	}

	return os.Open(path)
}

func (l *Local) Remove(_ context.Context, name string) error {
	path, err := l.resolve(name)
	if nil != err {
		return err
	}

	if err := os.Remove(path); nil != err && !errors.Is(err, fs.ErrNotExist) {
		return err
	}

	return nil
}

func (l *Local) List(_ context.Context, prefix string) ([]ObjectInfo, error) {
	var objects []ObjectInfo

	err := filepath.WalkDir(l.root, func(path string, entry fs.DirEntry, err error) error {
		if nil != err {
			return err
		}
		if entry.IsDir() {
			return nil
		}

		name := filepath.ToSlash(strings.TrimPrefix(path, l.root+string(os.PathSeparator)))
		if !strings.HasPrefix(name, prefix) {
			return nil
		}

		// Uncommitted objects are invisible until their rename lands.
		if strings.HasPrefix(filepath.Base(path), ".") {
			return nil
		}

		info, err := entry.Info()
		if nil != err {
			return err
		}

		objects = append(objects, ObjectInfo{Name: name, Size: info.Size(), Modified: info.ModTime()})
		return nil
	})
	if nil != err {
		return nil, err
	}

	sort.Slice(objects, func(i, j int) bool { return objects[i].Name < objects[j].Name })
	return objects, nil
}

func (l *Local) Stat(_ context.Context, name string) (ObjectInfo, error) {
	path, err := l.resolve(name)
	if nil != err {
		return ObjectInfo{}, err
	}

	info, err := os.Stat(path)
	if nil != err {
		return ObjectInfo{}, err
	}

	return ObjectInfo{Name: name, Size: info.Size(), Modified: info.ModTime()}, nil
}

func (l *Local) Close() error { return nil }

/*!
 * localWriter publishes on Commit and cleans up on Abort.
 */
type localWriter struct {
	file      *os.File
	final     string
	committed bool
}

func (w *localWriter) Write(p []byte) (int, error) { return w.file.Write(p) }

func (w *localWriter) Commit() error {
	if w.committed {
		return nil
	}

	// fsync the file before the rename, then the parent directory after it.
	// Without the second one the rename itself can be lost in a power failure,
	// leaving a durable temp file and no object.
	if err := w.file.Sync(); nil != err {
		w.file.Close()
		os.Remove(w.file.Name())
		return fmt.Errorf("syncing object: %w", err)
	}

	if err := w.file.Close(); nil != err {
		os.Remove(w.file.Name())
		return fmt.Errorf("closing object: %w", err)
	}

	if err := os.Rename(w.file.Name(), w.final); nil != err {
		os.Remove(w.file.Name())
		return fmt.Errorf("publishing object: %w", err)
	}

	w.committed = true
	return syncDir(filepath.Dir(w.final))
}

func (w *localWriter) Abort() error {
	if w.committed {
		return nil
	}

	w.committed = true
	w.file.Close()
	return os.Remove(w.file.Name())
}

func syncDir(dir string) error {
	handle, err := os.Open(dir)
	if nil != err {
		return err
	}
	defer handle.Close()

	// Directory fsync is not supported everywhere; a backend that cannot do it
	// is not a reason to fail a write that otherwise succeeded.
	if err := handle.Sync(); nil != err && !errors.Is(err, os.ErrInvalid) {
		return nil
	}

	return nil
}
