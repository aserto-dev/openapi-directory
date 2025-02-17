package openapi

import (
	_ "embed"
	"net/http"
	"strings"
	"sync"
	"text/template"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/pkg/errors"
	"github.com/rs/zerolog"
	"github.com/samber/lo"
)

type Server struct {
	Scheme string
	Host   string
}

//go:embed openapi.json
var static []byte

// Byte alice with the openapi.json file.
func Static() []byte {
	return static
}

var once = sync.Once{}

// Handler to serve the OpenAPI specification file.
//
// NOTE:
// The set of exposed services is determined and cached the first time
// this function is called. Calling it a second time with different arguments
// will have no effect.
func OpenApiHandler(svc ...string) func(w http.ResponseWriter, r *http.Request) {
	var (
		tmpl *template.Template
		err  error
	)

	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Content-Type", "application/json")

		once.Do(func() {
			tmpl, err = buildTemplate(svc...)
		})
		if err != nil {
			zerolog.Ctx(r.Context()).Err(err).Msg("failed to build template")
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}

		server := Server{
			Host:   r.Host,
			Scheme: scheme(r),
		}

		if err := tmpl.Execute(w, server); err != nil {
			zerolog.Ctx(r.Context()).Err(err).Msg("failed to execute template")
			http.Error(w, "Internal Server Error", http.StatusInternalServerError)
			return
		}
	}
}

func buildTemplate(svc ...string) (*template.Template, error) {
	content := static

	if len(svc) > 0 {
		// filter the OpenAPI spec to only keep the specified services.
		if c, err := filter(content, svc...); err != nil {
			return nil, err
		} else {
			content = c
		}
	}

	return template.New("openapi.json").Parse(string(content))
}

func filter(body []byte, svc ...string) ([]byte, error) {
	svc = lo.Map(svc, func(s string, _ int) string { return "directory." + s + "." })

	spec, err := parseSpec(body)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse openapi spec")
	}

	for path, item := range spec.Paths.Map() {
		ops := []**openapi3.Operation{&item.Get, &item.Put, &item.Post, &item.Delete, &item.Options, &item.Head, &item.Patch, &item.Trace}
		for _, op := range ops {
			if *op != nil && !hasAnyPrefix((*op).OperationID, svc...) {
				*op = nil
			}
		}

		if lo.Count(ops, nil) == len(ops) {
			// all operations are nil. Delete the path.
			spec.Paths.Delete(path)
		}
	}

	return spec.MarshalJSON()
}

func parseSpec(body []byte) (*openapi3.T, error) {
	loader := openapi3.NewLoader()
	return loader.LoadFromData(body)
}

func hasAnyPrefix(opID string, prefix ...string) bool {
	for _, p := range prefix {
		if strings.HasPrefix(opID, p) {
			return true
		}
	}
	return false
}

func scheme(r *http.Request) string {
	scheme := r.Header.Get("X-Forwarded-Proto")
	if scheme == "" {
		if r.TLS == nil {
			scheme = "http"
		} else {
			scheme = "https"
		}
	}

	return scheme
}
