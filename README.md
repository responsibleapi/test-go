# OpenAPI response Go test helper

HTTP/API boundary test helper for http servers

It sends requests with the standard library `*http.Client` and validates responses with `github.com/getkin/kin-openapi`.

## Install

```sh
go get github.com/responsibleapi/test-go
```

## Usage

```go
package apitest

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"

	responsibleapi "github.com/responsibleapi/test-go"
)

var (
	apiBaseURL  string
	apiClient   http.Client
	responsible *responsibleapi.Responsible
)

func TestMain(m *testing.M) {
	os.Exit(run(m))
}

func run(m *testing.M) int {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	env, err := startTestEnv(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "start api test environment: %v\n", err)
		return 1
	}
	apiBaseURL = "http://" + env.server.PublicAddr()
	apiClient = http.Client{Timeout: 10 * time.Second}

	responsible, err = responsibleapi.LoadFromFile("../../openapi.yaml", responsibleapi.Options{
		BaseURL: apiBaseURL,
		Client:  &apiClient,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "load openapi spec: %v\n", err)
		return 1
	}

	code := m.Run()

	if err := env.Close(); err != nil {
		fmt.Fprintf(os.Stderr, "close api test environment: %v\n", err)
		code = 1
	}
	return code
}

func TestGetShow(t *testing.T) {
	t.Parallel()

	opts := responsibleapi.CheckOptions{
		OperationID: "getShow",
		Request: responsibleapi.RequestOptions{
			PathParams: map[string]string{"show_id": "abc123"},
		},
		Expected: http.StatusOK,
	}
	resp := responsible.Check(t, opts)
	defer func() {
		_ = resp.Body.Close()
	}()
}
```

---

ResponsibleAPI turns OpenAPI into SDKs, docs, MCP servers, and CLIs: https://responsibleapi.com
