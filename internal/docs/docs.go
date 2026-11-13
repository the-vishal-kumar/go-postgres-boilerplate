// Package docs serves the OpenAPI contract and a self-hosted Swagger UI.
package docs

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"io/fs"

	swaggerFiles "github.com/swaggo/files/v2"
)

const (
	title       = "Loan API (Go, PostgreSQL)"
	stackLine   = "Go and PostgreSQL implementation of the loan API."
	contentType = "application/json; charset=utf-8"
)

//go:embed openapi.json
var contract []byte

//go:embed index.html
var indexHTML []byte

//go:embed swagger-initializer.js
var initializerJS []byte

// ContentType is the media type of the document returned by Spec.
func ContentType() string { return contentType }

// Spec returns the contract with only info.title and info.description changed,
// so every implementation of the API publishes the same operations and schemas.
func Spec() ([]byte, error) {
	dec := json.NewDecoder(bytes.NewReader(contract))
	dec.UseNumber() // keep numbers exactly as written
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		return nil, fmt.Errorf("decode contract: %w", err)
	}
	info, ok := doc["info"].(map[string]any)
	if !ok {
		return nil, fmt.Errorf("contract has no info object")
	}
	info["title"] = title
	desc, _ := info["description"].(string)
	if desc != "" {
		desc = stackLine + "\n\n" + desc
	} else {
		desc = stackLine
	}
	info["description"] = desc
	return json.Marshal(doc)
}

// Index is the Swagger UI page.
func Index() []byte { return indexHTML }

// Initializer is the script that points Swagger UI at /docs/openapi.json.
func Initializer() []byte { return initializerJS }

// assets are the Swagger UI files the page needs; nothing else is exposed.
var assets = map[string]bool{
	"swagger-ui.css":                  true,
	"swagger-ui-bundle.js":            true,
	"swagger-ui-standalone-preset.js": true,
	"favicon-16x16.png":               true,
	"favicon-32x32.png":               true,
}

// Asset returns a Swagger UI distribution file by name.
func Asset(name string) ([]byte, bool) {
	if !assets[name] {
		return nil, false
	}
	b, err := fs.ReadFile(swaggerFiles.FS, name)
	if err != nil {
		return nil, false
	}
	return b, true
}
