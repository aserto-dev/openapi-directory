package openapi

import (
	"embed"
	"net/http"
	"sync"
	"text/template"
)

//go:embed openapi.json
var staticAssets embed.FS

// Static embedded FS service openapi.json file.
func Static() embed.FS {
	return staticAssets
}

// Handler to serve the OpenAPI specification file.
func OpenApiHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Add("Content-Type", "application/json")

	template, err := buildTemplate()
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}

	server := Server{
		Host:   r.Host,
		Scheme: scheme(r),
	}

	err = template.Execute(w, server)
	if err != nil {
		http.Error(w, "Internal Server Error", http.StatusInternalServerError)
		return
	}
}

func scheme(r *http.Request) string {
	var scheme string
	scheme = r.Header.Get("X-Forwarded-Proto")
	if scheme == "" {
		if r.TLS == nil {
			scheme = "http"
		} else {
			scheme = "https"
		}
	}

	return scheme
}

func staticString() (string, error) {
	data, err := staticAssets.ReadFile("openapi.json")
	if err != nil {
		return "", err
	}
	return string(data), nil
}

type Server struct {
	Scheme string
	Host   string
}

func buildTemplate() (*template.Template, error) {
	once := sync.OnceValues(func() (*template.Template, error) {
		data, err := staticString()
		if err != nil {
			return nil, err
		}
		return template.New("openapi.json").Parse(data)
	})
	return once()
}
