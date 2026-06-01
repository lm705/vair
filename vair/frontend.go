package main

import _ "embed"

// indexHTML is the entire single-page UI (HTML + CSS + JS + i18n), embedded
// from web/index.html at build time. Served verbatim by the "/" route. Kept as
// a real .html file (rather than a Go raw-string) for editor tooling/syntax
// highlighting; this file is intentionally untagged so the UI is available on
// every platform that runs the HTTP server.
//
//go:embed web/index.html
var indexHTML string
