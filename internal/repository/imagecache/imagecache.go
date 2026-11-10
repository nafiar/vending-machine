// Package imagecache is the port to the local copy of product images. Every
// image gets a new random name on the server when it changes, so a copy never
// goes stale: the kiosk shows its pictures right after a reboot, even offline
// (Edge kiosk mode keeps no browser cache). The disk store lives in
// imagecache/file.
package imagecache

// Repository stores images by their path under /uploads/, e.g.
// "products/3f9c...e1.png". Callers pass only validated paths.
type Repository interface {
	Get(rel string) ([]byte, error)
	Put(rel string, data []byte) error
}
