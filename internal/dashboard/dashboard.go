// Package dashboard holds the management dashboard page that the image build
// produces from dashboard/ with scripts/dashboard.sh.
package dashboard

import (
	"embed"
	"io/fs"
)

//go:embed all:dist
var dist embed.FS

// HTML returns the built dashboard page, or nil when the binary was built
// without one.
func HTML() []byte {
	page, err := fs.ReadFile(dist, "dist/management.html")
	if err != nil {
		return nil
	}
	return page
}
