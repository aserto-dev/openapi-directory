package openapi

import "embed"

//go:embed openapi.json
var staticAssets embed.FS

// Static embedded FS service openapi.json file.
func Static() embed.FS {
	return staticAssets
}
