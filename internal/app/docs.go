package app

import (
	"io/fs"
	"net/http"

	"github.com/bjornnorgaard/golden-path-go/api"
)

// mountDocs serves the contracts and an API reference page. Only mounted when
// DOCS_ENABLED is set, and still behind authentication.
func mountDocs(r *router) {
	sub, err := fs.Sub(api.Contracts, "openapi")
	if err != nil {
		panic(err) // embedded filesystem, cannot fail
	}
	r.Handle("GET /openapi/", http.StripPrefix("/openapi/", http.FileServerFS(sub)))
	r.HandleFunc("GET /docs", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(docsPage))
	})
}

const docsPage = `<!doctype html>
<html><head><meta charset="utf-8"><title>golden-path-go API reference</title></head>
<body>
<script id="api-reference" data-configuration='{"sources":[{"title":"Todos","url":"/openapi/todos.openapi.yaml"},{"title":"Users","url":"/openapi/users.openapi.yaml"}]}'></script>
<script src="https://cdn.jsdelivr.net/npm/@scalar/api-reference"></script>
</body></html>`
