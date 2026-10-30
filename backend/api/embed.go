// Package api OpenAPI belgesini binary'ye gömer (GET /openapi.yaml).
package api

import _ "embed"

// OpenAPI belge içeriğidir (api/openapi.yaml).
//
//go:embed openapi.yaml
var OpenAPI []byte
