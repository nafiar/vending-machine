// Package file keeps product images in a folder on disk.
package file

import (
	"os"
	"path/filepath"

	"vending-machine/internal/repository/imagecache"
)

// File is the image folder (data/cache/uploads). Build one in cmd/main.
type File struct{ dir string }

var _ imagecache.Repository = (*File)(nil)

// New returns the store for dir; the folder is created on the first Put.
func New(dir string) *File { return &File{dir: dir} }

func (f *File) path(rel string) string { return filepath.Join(f.dir, filepath.FromSlash(rel)) }

// Get reads a stored image.
func (f *File) Get(rel string) ([]byte, error) { return os.ReadFile(f.path(rel)) }

// Put writes an image through a temporary file, so a reader never sees half of it.
func (f *File) Put(rel string, data []byte) error {
	local := f.path(rel)
	if err := os.MkdirAll(filepath.Dir(local), 0o755); err != nil {
		return err
	}
	tmp := local + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, local)
}
