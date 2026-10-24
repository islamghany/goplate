// Package api holds the OpenAPI contract (ADR-0014).
//
// openapi.yaml is written by hand and is the source of truth for the HTTP
// API. It is embedded into the binary and served at /openapi.yaml, with a
// browsable reference at /docs (when HTTP_DOCS_ENABLED=true). The drift test
// in cmd/api fails when a route exists in code but not in the spec, or the
// other way round.
package api

import (
	_ "embed"
	"net/http"
)

// Spec is the raw OpenAPI document.
//
//go:embed openapi.yaml
var Spec []byte

// SpecHandler serves the OpenAPI document.
func SpecHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/yaml")
		_, _ = w.Write(Spec)
	})
}

// docsPage renders the spec with Scalar, loaded from a pinned CDN version.
const docsPage = `<!doctype html>
<html>
<head>
  <title>goplate API reference</title>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
</head>
<body>
  <script id="api-reference" data-url="/openapi.yaml"></script>
  <script src="https://cdn.jsdelivr.net/npm/@scalar/api-reference@1.73.1"></script>
</body>
</html>`

// DocsHandler serves the interactive API reference.
func DocsHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(docsPage))
	})
}
