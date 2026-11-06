package audit

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static/*
var embeddedWeb embed.FS

func webHandler() http.Handler {
	root, err := fs.Sub(embeddedWeb, "static")
	if err != nil {
		panic(err)
	}
	return http.FileServer(http.FS(root))
}
