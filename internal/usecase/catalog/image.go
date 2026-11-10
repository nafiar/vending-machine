package catalog

import (
	"context"
	"regexp"
	"strings"

	"vending-machine/internal/model"
)

// Product images come from the server's /uploads/. The agent keeps a copy of
// each, so the kiosk shows its pictures right after a reboot.

var (
	uploadDirPart  = regexp.MustCompile(`^[A-Za-z0-9_-]{1,64}$`)
	uploadFilePart = regexp.MustCompile(`^[A-Za-z0-9_-]{1,128}\.(png|jpg|jpeg|svg|webp)$`)
)

// validUpload accepts paths such as products/3f9c...e1.png and nothing that
// could leave the cache folder.
func validUpload(rel string) bool {
	parts := strings.Split(rel, "/")
	if len(parts) == 0 || len(parts) > 3 {
		return false
	}
	for _, p := range parts[:len(parts)-1] {
		if !uploadDirPart.MatchString(p) {
			return false
		}
	}
	return uploadFilePart.MatchString(strings.ToLower(parts[len(parts)-1]))
}

func (s *Service) Image(ctx context.Context, rel string) ([]byte, error) {
	if !validUpload(rel) {
		return nil, model.ErrNotFound
	}
	if data, err := s.images.Get(rel); err == nil {
		return data, nil
	}
	cctx, cancel := context.WithTimeout(ctx, s.t.Image)
	defer cancel()
	data, err := s.server.Image(cctx, rel)
	if err != nil {
		return nil, err
	}
	if err := s.images.Put(rel, data); err != nil {
		s.log.Debug("image not kept on disk", "path", rel, "err", err)
	}
	return data, nil
}
