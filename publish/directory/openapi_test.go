package openapi_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	openapi "github.com/aserto-dev/openapi-directory/publish/directory"
	"github.com/samber/lo"
	req "github.com/stretchr/testify/require"
	"github.com/tidwall/gjson"
)

func TestFilter(t *testing.T) {
	for _, services := range [][]string{
		{"reader"},
		{"writer"},
		{"model"},
		{"assertion"},
		{"reader", "writer"},
		{"model", "writer"},
		{"reader", "writer", "model", "assertion"},
	} {
		t.Run(strings.Join(services, ":"), func(tt *testing.T) {
			require := req.New(tt)

			b, err := openapi.Filter(openapi.Static(), services...)
			require.NoError(err)

			expected := lo.Map(services,
				func(s string, _ int) string { return "directory." + s + "." },
			)

			opIDs := lo.Map(gjson.GetBytes(b, "paths.@dig:operationId").Array(),
				func(v gjson.Result, _ int) string { return v.String() },
			)
			require.NotEmpty(opIDs)

			// Each remaining operation ID must match one of the expected services.
			for _, opID := range opIDs {
				require.True(openapi.HasAnyPrefix(opID, expected...))
			}

			// Each of the expected services must match at least one operation ID.
			for _, svc := range expected {
				require.True(hasPrefixMatches(svc, opIDs...))
			}
		})
	}
}

func TestHandler(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(openapi.OpenAPIHandler(3030, "reader")))
	t.Cleanup(server.Close)

	require := req.New(t)

	resp, err := http.Get(server.URL) //nolint:noctx
	require.NoError(err)
	t.Cleanup(func() { resp.Body.Close() })

	body, err := io.ReadAll(resp.Body)
	require.NoError(err)

	t.Logf("Response: %s", body)

	opIDs := lo.Map(gjson.GetBytes(body, "paths.@dig:operationId").Array(),
		func(v gjson.Result, _ int) string { return v.String() },
	)
	require.NotEmpty(opIDs)

	for _, opID := range opIDs {
		require.True(openapi.HasAnyPrefix(opID, "directory.reader"))
	}
}

func hasPrefixMatches(prefix string, vals ...string) bool {
	for _, val := range vals {
		if strings.HasPrefix(val, prefix) {
			return true
		}
	}
	return false
}
