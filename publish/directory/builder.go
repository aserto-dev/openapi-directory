package openapi

import (
	"strings"
	"text/template"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/pkg/errors"
	"github.com/samber/lo"
)

type templateBuilder struct {
	tmpl *template.Template
	err  error

	// No data is sent on this channel.
	// It remains open while the build is in progress and is then
	// closed, inidicating that tmpl and err hold the results of
	// the build.
	ready chan struct{}
}

func newTemplateBuilder() *templateBuilder {
	return &templateBuilder{
		ready: make(chan struct{}),
	}
}

func (t *templateBuilder) Build(port string, svc ...string) {
	var err error
	content := static

	if len(svc) > 0 {
		// filter the OpenAPI spec to only keep the specified services.
		content, err = filter(content, svc...)
	}

	if err == nil {
		t.tmpl, t.err = template.New("openapi.json").Parse(string(content))
	}

	// signals that the build is complete.
	close(t.ready)
}

func (t *templateBuilder) Get() (*template.Template, error) {
	// wait for the build to complete.
	<-t.ready

	return t.tmpl, t.err
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

		if len(ops) == lo.CountBy(ops, func(pOp **openapi3.Operation) bool { return *pOp == nil }) {
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
