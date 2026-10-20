//go:build embedded

package web

import (
	"embed"
	"io/fs"
	"net/http"
)

// embedded holds the dashboard that was compiled into this binary. The all:
// prefix includes files Vite names with a leading dot or underscore, which the
// default embed rules would otherwise skip and a bundle can legitimately
// contain.
//
// This file only participates in a build with -tags embedded, and only such a
// build needs backend-go/internal/web/dist to exist. The default build uses
// static_disk.go and never references this directory, so a checkout without a
// build still compiles — which is the whole point of keeping dist out of the
// repository (D3).
//
//go:embed all:dist
var embedded embed.FS

// distFS exposes the compiled-in dashboard as an http.FileSystem rooted at the
// dist directory. It is always present in an embedded build: the compiler
// refused to produce the binary otherwise.
func distFS() (http.FileSystem, bool) {
	sub, err := fs.Sub(embedded, "dist")
	if err != nil {
		return nil, false
	}
	return http.FS(sub), true
}
