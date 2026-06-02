package responsible

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestCheckValidatesResponse(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if got, want := req.Method, http.MethodGet; got != want {
			t.Fatalf("method = %q, want %q", got, want)
		}
		if got, want := req.URL.Path, "/shows/show-1"; got != want {
			t.Fatalf("path = %q, want %q", got, want)
		}
		if got, want := req.URL.Query().Get("include"), "episodes"; got != want {
			t.Fatalf("query include = %q, want %q", got, want)
		}
		if got, want := req.Header.Get("X-Test"), "1"; got != want {
			t.Fatalf("X-Test = %q, want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"show-1","title":"Responsible"}`))
	}))
	defer server.Close()

	checker := newTestResponsible(t, server)
	res := checker.Check(t, CheckOptions{
		OperationID: "getShow",
		Request: RequestOptions{
			PathParams: map[string]string{"show_id": "show-1"},
			Query:      url.Values{"include": []string{"episodes"}},
			Headers:    http.Header{"X-Test": []string{"1"}},
		},
		Expected: http.StatusOK,
	})

	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(body), `{"id":"show-1","title":"Responsible"}`; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestCheckReturnsStatusErrorBeforeResponseValidation(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"error":"missing"}`))
	}))
	defer server.Close()

	checker := newTestResponsible(t, server)
	res, err := checker.check(t, CheckOptions{
		Ctx:         context.Background(),
		OperationID: "getShow",
		Request:     RequestOptions{PathParams: map[string]string{"show_id": "missing"}},
		Expected:    http.StatusOK,
	})
	if res == nil {
		t.Fatalf("response is nil")
	}
	var statusErr *StatusError
	if !errors.As(err, &statusErr) {
		t.Fatalf("err = %#v, want StatusError", err)
	}
	if got, want := statusErr.Actual, http.StatusNotFound; got != want {
		t.Fatalf("actual status = %d, want %d", got, want)
	}
	if !strings.Contains(err.Error(), `{"error":"missing"}`) {
		t.Fatalf("err = %q, want response body", err)
	}
}

func TestCheckRejectsInvalidResponseBody(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"id":"show-1"}`))
	}))
	defer server.Close()

	checker := newTestResponsible(t, server)
	res, err := checker.check(t, CheckOptions{
		Ctx:         context.Background(),
		OperationID: "getShow",
		Request:     RequestOptions{PathParams: map[string]string{"show_id": "show-1"}},
		Expected:    http.StatusOK,
	})
	if err == nil {
		t.Fatalf("err = nil, want validation error")
	}
	if !strings.Contains(err.Error(), "validate response") {
		t.Fatalf("err = %q, want validation context", err)
	}

	body, readErr := io.ReadAll(res.Body)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if got, want := string(body), `{"id":"show-1"}`; got != want {
		t.Fatalf("body = %q, want %q", got, want)
	}
}

func TestOperationNewRequestSendsJSONBody(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if got, want := req.Method, http.MethodPost; got != want {
			t.Fatalf("method = %q, want %q", got, want)
		}
		if got, want := req.Header.Get("Content-Type"), "application/json"; got != want {
			t.Fatalf("Content-Type = %q, want %q", got, want)
		}
		body, err := io.ReadAll(req.Body)
		if err != nil {
			t.Fatal(err)
		}
		if got, want := string(body), `{"name":"new show"}`; got != want {
			t.Fatalf("body = %q, want %q", got, want)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"show-2","title":"new show"}`))
	}))
	defer server.Close()

	checker := newTestResponsible(t, server)
	res := checker.Check(t, CheckOptions{
		OperationID: "createShow",
		Request:     RequestOptions{JSON: map[string]string{"name": "new show"}},
		Expected:    http.StatusCreated,
	})
	defer res.Body.Close()
}

func TestCheckReportsErrorsThroughTestingHelper(t *testing.T) {
	t.Parallel()

	checker := newTestResponsible(t, nil)
	recorder := &recordingT{}
	res := checker.Check(recorder, CheckOptions{
		OperationID: "getShow",
		Request:     RequestOptions{},
		Expected:    http.StatusOK,
	})
	if res != nil {
		t.Fatalf("response = %#v, want nil", res)
	}
	if got, want := recorder.helpers, 1; got != want {
		t.Fatalf("Helper calls = %d, want %d", got, want)
	}
	if !strings.Contains(recorder.message, "missing path params: show_id") {
		t.Fatalf("message = %q, want missing path param error", recorder.message)
	}
}

func TestOperationNewRequestRequiresPathParams(t *testing.T) {
	t.Parallel()

	checker := newTestResponsible(t, nil)
	_, err := checker.Operation("getShow").NewRequest(context.Background(), RequestOptions{})
	if err == nil {
		t.Fatalf("err = nil, want missing path param error")
	}
	if !strings.Contains(err.Error(), "show_id") {
		t.Fatalf("err = %q, want path param name", err)
	}
}

func TestOperationPanicsForUnknownOperationID(t *testing.T) {
	t.Parallel()

	checker := newTestResponsible(t, nil)
	defer func() {
		recovered := recover()
		if recovered == nil {
			t.Fatalf("Operation() did not panic")
		}
		message, ok := recovered.(string)
		if !ok {
			t.Fatalf("panic = %#v, want string", recovered)
		}
		if !strings.Contains(message, `Operation("missing")`) {
			t.Fatalf("panic = %q, want requested operation", message)
		}
		if !strings.Contains(message, "available operationIds: createShow, getShow") {
			t.Fatalf("panic = %q, want available operations", message)
		}
	}()

	checker.Operation("missing")
}

func newTestResponsible(t *testing.T, server *httptest.Server) *Responsible {
	t.Helper()

	options := Options{}
	if server != nil {
		options.BaseURL = server.URL
		options.Client = server.Client()
	}
	checker, err := LoadFromData([]byte(testSpec), options)
	if err != nil {
		t.Fatal(err)
	}
	return checker
}

type recordingT struct {
	helpers int
	message string
}

func (t *recordingT) Helper() {
	t.helpers++
}

func (t *recordingT) Fatalf(format string, args ...any) {
	t.message = fmt.Sprintf(format, args...)
}

const testSpec = `
openapi: 3.0.3
info:
  title: test
  version: 1.0.0
paths:
  /shows/{show_id}:
    get:
      operationId: getShow
      parameters:
        - name: show_id
          in: path
          required: true
          schema:
            type: string
        - name: include
          in: query
          schema:
            type: string
      responses:
        '200':
          description: found
          content:
            application/json:
              schema:
                type: object
                required: [id, title]
                additionalProperties: false
                properties:
                  id:
                    type: string
                  title:
                    type: string
        '404':
          description: missing
  /shows:
    post:
      operationId: createShow
      requestBody:
        required: true
        content:
          application/json:
            schema:
              type: object
              required: [name]
              additionalProperties: false
              properties:
                name:
                  type: string
      responses:
        '201':
          description: created
          content:
            application/json:
              schema:
                type: object
                required: [id, title]
                additionalProperties: false
                properties:
                  id:
                    type: string
                  title:
                    type: string
`
