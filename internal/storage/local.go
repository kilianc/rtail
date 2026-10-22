/*!
 * Local-disk implementation of storage.Backend.
 *
 * Object names are slash-separated and map onto a directory tree under root,
 * so the on-disk layout stays browsable with `ls`. That is a feature, not an
 * accident: handing someone a directory of Parquet files
 * they can open in DuckDB without rtail installed is the whole anti-lock-in
 * argument.
 */

package storage

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"
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

	if err := mkdirAll(absolute); nil != err {
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

	if err := mkdirAll(filepath.Dir(path)); nil != err {
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

func (l *Local) Open(_ context.Context, name string) (Reader, error) {
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

/*!
 * Sweep removes temp files abandoned under prefix before the cutoff.
 *
 * Create writes into a dot-prefixed temp file beside the destination, and only
 * Commit or Abort ever removes it — so a process killed mid-write leaves one
 * behind that nothing will look at again. The cutoff is what keeps this from
 * racing a write still in progress: such a file is younger than any sane
 * cutoff, because it was created moments ago and is written as it goes.
 */
func (l *Local) Sweep(_ context.Context, prefix string, before time.Time) (int, error) {
	removed := 0

	err := filepath.WalkDir(l.root, func(path string, entry fs.DirEntry, err error) error {
		if nil != err {
			// A directory removed while being walked is not a reason to stop.
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if entry.IsDir() {
			return nil
		}

		name := filepath.ToSlash(strings.TrimPrefix(path, l.root+string(os.PathSeparator)))
		if !strings.HasPrefix(name, prefix) {
			return nil
		}

		base := filepath.Base(path)
		if !strings.HasPrefix(base, ".") || !strings.Contains(base, ".tmp-") {
			return nil
		}

		info, err := entry.Info()
		if nil != err {
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if !info.ModTime().Before(before) {
			return nil
		}

		if err := os.Remove(path); nil != err && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
		removed++

		return nil
	})

	return removed, err
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

/*!
 * mkdirAll is os.MkdirAll that also makes the directories it creates durable.
 *
 * A directory's existence is an entry in its parent, and that entry is only
 * on disk once the parent is fsynced. Commit fsyncs the directory an object
 * lands in, which persists the object's own entry — but if the day directory
 * was created a moment earlier, nothing persisted the entries leading to it,
 * and a power loss can take `streams/api/2026/10/21` away with every object
 * inside it, catalog rows still pointing at them.
 */
func mkdirAll(dir string) error {
	// Everything from the deepest ancestor that already exists downward is new,
	// and each new directory needs its parent synced.
	var created []string
	for current := dir; ; {
		_, err := os.Stat(current)
		if nil == err {
			break
		}
		if !errors.Is(err, fs.ErrNotExist) {
			return err
		}

		created = append(created, current)

		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		current = parent
	}

	if err := os.MkdirAll(dir, 0o755); nil != err {
		return err
	}

	for _, path := range created {
		if err := syncDir(filepath.Dir(path)); nil != err {
			return err
		}
	}

	return nil
}

// fsync is a variable so tests can make it fail; nothing else replaces it.
var fsync = (*os.File).Sync

func syncDir(dir string) error {
	// Windows has no directory fsync at all, and NTFS journals directory
	// entries itself.
	if "windows" == runtime.GOOS {
		return nil
	}

	handle, err := os.Open(dir)
	if nil != err {
		return err
	}
	defer handle.Close()

	if err := fsync(handle); nil != err && !syncUnsupported(err) {
		return fmt.Errorf("syncing directory %s: %w", dir, err)
	}

	return nil
}

/*!
 * syncUnsupported recognises a filesystem saying it cannot fsync a directory,
 * as opposed to saying it tried and failed.
 *
 * Only the first is safe to ignore. Some filesystems answer EINVAL or ENOTSUP
 * to fsync on a directory because the operation means nothing to them, and
 * that is not a reason to fail a write that otherwise succeeded. An EIO is the
 * opposite: the rename this sync was meant to persist may not survive a power
 * loss, and the caller is about to register the object in the catalog on the
 * strength of it.
 */
func syncUnsupported(err error) bool {
	return errors.Is(err, syscall.EINVAL) || errors.Is(err, errors.ErrUnsupported)
}
