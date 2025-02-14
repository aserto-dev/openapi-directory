package openapi

import (
	_ "embed"
	"net/http"
	"strings"
	"sync"
	"text/template"

	"github.com/hashicorp/go-multierror"
	openapi "github.com/pb33f/libopenapi"
	v3 "github.com/pb33f/libopenapi/datamodel/high/v3"
	"github.com/pkg/errors"
	"github.com/rs/zerolog"
	"github.com/samber/lo"
)

type Server struct {
	Scheme string
	Host   string
}

//go:embed openapi.json
var staticString string

// Static string value of the openapi.json file.
func Static() string {
	return staticString
}

var once = sync.Once{}

// Handler to serve the OpenAPI specification file.
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
	content := staticString

	if len(svc) > 0 {
		// filter the OpenAPI spec to only keep the specified services.
		if c, err := filter(content, svc...); err != nil {
			return nil, err
		} else {
			content = string(c)
		}
	}

	return template.New("openapi.json").Parse(content)
}

func filter(body string, svc ...string) ([]byte, error) {
	svc = lo.Map(svc, func(s string, _ int) string { return "directory." + s + "." })

	spec, err := parseSpec([]byte(body))
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse openapi spec")
	}

	paths := spec.Model.Paths.PathItems
	emptyPaths := []string{}

	for pair := paths.First(); pair != nil; pair = pair.Next() {
		item := pair.Value()
		ops := []**v3.Operation{&item.Get, &item.Put, &item.Post, &item.Delete, &item.Options, &item.Head, &item.Patch, &item.Trace}
		for _, op := range ops {
			if *op != nil && !hasAnyPrefix((*op).OperationId, svc...) {
				*op = nil
			}
		}

		if lo.Count(ops, nil) == len(ops) {
			// all operations are nil, mark for removal.
			emptyPaths = append(emptyPaths, pair.Key())
		}
	}

	for _, path := range emptyPaths {
		paths.Delete(path)
	}

	return spec.Model.RenderJSON("  ")
}

func parseSpec(body []byte) (*openapi.DocumentModel[v3.Document], error) {
	doc, err := openapi.NewDocument(body)
	if err != nil {
		return nil, err
	}

	model, errs := doc.BuildV3Model()
	if len(errs) > 0 {
		return nil, multierror.Append(err, errs...)
	}

	return model, nil
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
