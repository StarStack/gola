# Changelog

## v0.1.0 — 2026-09-20

Initial GoLa version.

- HTTP routing, route groups, request contexts, and middleware chains built directly on Go's `net/http`.
- JSON, XML, query, URI, and form binding with input limits and optional validation; response helpers and standard handler adapters.
- Bounded multipart uploads with request-scoped temporary-file cleanup, controlled file storage, attachment downloads, and static assets with standard HTTP range and conditional responses.
- HTML templates, XML responses, SSE, and a separate WebSocket lifecycle example.
- Request IDs, structured logging, panic recovery, trusted proxy configuration, and optional CORS middleware.
- Optional gzip, bounded process-local rate limiting, JSON request metrics, and explicit OpenAPI 3.1.1 documents.
- An isolated OpenTelemetry adapter for HTTP request tracing.
- English and Chinese READMEs, usage guides, runnable examples, regression tests, fuzz targets, and benchmarks.
- MIT license, with copyright held by STARDATA INTERNATIONAL PTE.LTD.
