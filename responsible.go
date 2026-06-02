package responsible

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
	"github.com/getkin/kin-openapi/openapi3filter"
	"github.com/getkin/kin-openapi/routers"
	"github.com/getkin/kin-openapi/routers/gorillamux"
)

var pathParamRE = regexp.MustCompile(`\{([^{}]+)\}`)

type Options struct {
	BaseURL           string
	Client            *http.Client
	ValidationOptions *openapi3filter.Options
}

type Responsible struct {
	spec              *openapi3.T
	router            routers.Router
	client            *http.Client
	baseURL           *url.URL
	operations        map[string]*Operation
	orderedOperations []*Operation
	validationOptions openapi3filter.Options
}

type Operation struct {
	owner     *Responsible
	route     *routers.Route
	method    string
	path      string
	operation *openapi3.Operation
}

type RequestOptions struct {
	PathParams  map[string]string
	Query       url.Values
	Headers     http.Header
	JSON        any
	Text        *string
	Body        io.Reader
	ContentType string
}

type TestingT interface {
	Helper()
	Fatalf(format string, args ...any)
}

type CheckOptions struct {
	Ctx         context.Context
	OperationID string
	Request     RequestOptions
	Expected    int
}

type StatusError struct {
	Expected int
	Actual   int
	Body     []byte
}

type requestMetadata struct {
	route      *routers.Route
	pathParams map[string]string
}

type requestMetadataKey struct{}

func New(spec *openapi3.T, options Options) (*Responsible, error) {
	if spec == nil {
		return nil, errors.New("openapi spec cannot be nil")
	}
	if spec.Paths == nil {
		return nil, errors.New("openapi spec paths cannot be nil")
	}
	if err := spec.Validate(context.Background()); err != nil {
		return nil, fmt.Errorf("invalid openapi spec: %w", err)
	}

	router, err := gorillamux.NewRouter(spec)
	if err != nil {
		return nil, err
	}

	client := options.Client
	if client == nil {
		client = http.DefaultClient
	}

	var baseURL *url.URL
	if options.BaseURL != "" {
		parsed, err := url.Parse(options.BaseURL)
		if err != nil {
			return nil, fmt.Errorf("invalid base URL: %w", err)
		}
		baseURL = parsed
	}

	validationOptions := openapi3filter.Options{IncludeResponseStatus: true}
	if options.ValidationOptions != nil {
		validationOptions = *options.ValidationOptions
	}

	responsible := &Responsible{
		spec:              spec,
		router:            router,
		client:            client,
		baseURL:           baseURL,
		operations:        make(map[string]*Operation),
		validationOptions: validationOptions,
	}
	if err := responsible.collectOperations(); err != nil {
		return nil, err
	}
	return responsible, nil
}

func LoadFromFile(path string, options Options) (*Responsible, error) {
	spec, err := openapi3.NewLoader().LoadFromFile(path)
	if err != nil {
		return nil, err
	}
	return New(spec, options)
}

func LoadFromData(data []byte, options Options) (*Responsible, error) {
	spec, err := openapi3.NewLoader().LoadFromData(data)
	if err != nil {
		return nil, err
	}
	return New(spec, options)
}

func (responsible *Responsible) Operation(operationID string) *Operation {
	if responsible == nil {
		panic("responsible: Operation called on nil Responsible")
	}
	if operation := responsible.operations[operationID]; operation != nil {
		return operation
	}
	panic(fmt.Sprintf(
		"responsible: Operation(%q): operationId not found in OpenAPI spec; available operationIds: %s",
		operationID,
		responsible.availableOperationIDs(),
	))
}

func (responsible *Responsible) Operations() []*Operation {
	return slices.Clone(responsible.orderedOperations)
}

func (responsible *Responsible) Check(t TestingT, options CheckOptions) *http.Response {
	t = requireTestingT(t, "Check")

	res, err := responsible.check(t, options)
	if err != nil {
		t.Fatalf("%v", err)
	}
	return res
}

func (responsible *Responsible) check(t TestingT, options CheckOptions) (*http.Response, error) {
	req, err := responsible.Operation(options.OperationID).NewRequest(options.context(t), options.Request)
	if err != nil {
		return nil, err
	}
	return responsible.checkRequest(req, options.Expected)
}

func (responsible *Responsible) checkRequest(req *http.Request, status int) (*http.Response, error) {
	if responsible == nil {
		return nil, errors.New("responsible cannot be nil")
	}
	if req == nil {
		return nil, errors.New("request cannot be nil")
	}
	if status < 100 || status > 599 {
		return nil, fmt.Errorf("status must be in range 100..599: %d", status)
	}

	res, err := responsible.client.Do(req)
	if err != nil {
		return nil, err
	}

	body, err := readAndReplaceBody(res)
	if err != nil {
		return res, err
	}
	if res.StatusCode != status {
		return res, &StatusError{Expected: status, Actual: res.StatusCode, Body: body}
	}

	input, err := responsible.responseValidationInput(req, res, body)
	if err != nil {
		return res, err
	}
	if err := openapi3filter.ValidateResponse(req.Context(), input); err != nil {
		return res, fmt.Errorf("validate response: %w", err)
	}
	return res, nil
}

func (operation *Operation) NewRequest(ctx context.Context, options RequestOptions) (*http.Request, error) {
	if operation == nil {
		return nil, errors.New("operation cannot be nil")
	}

	path, err := replacePathParams(operation.path, options.PathParams)
	if err != nil {
		return nil, err
	}

	body, contentType, err := requestBody(options)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, operation.method, operation.owner.requestURL(path, options.Query), body)
	if err != nil {
		return nil, err
	}
	req.Header = cloneHeader(options.Headers)
	if contentType != "" && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", contentType)
	}

	meta := requestMetadata{
		route:      operation.route,
		pathParams: clonePathParams(options.PathParams),
	}
	return req.WithContext(context.WithValue(req.Context(), requestMetadataKey{}, meta)), nil
}

func (operation *Operation) Method() string {
	return operation.method
}

func (operation *Operation) Path() string {
	return operation.path
}

func (options CheckOptions) context(t TestingT) context.Context {
	if options.Ctx != nil {
		return options.Ctx
	}
	if provider, ok := t.(interface {
		Context() context.Context
	}); ok {
		return provider.Context()
	}
	return context.Background()
}

func (operation *Operation) OpenAPIOperation() *openapi3.Operation {
	return operation.operation
}

func (err *StatusError) Error() string {
	message := fmt.Sprintf("expected status %d, got %d", err.Expected, err.Actual)
	if len(err.Body) == 0 {
		return message
	}
	return message + ". " + string(err.Body)
}

func requireTestingT(t TestingT, helper string) TestingT {
	if t == nil {
		panic(fmt.Sprintf("responsible: %s requires a testing helper", helper))
	}
	t.Helper()
	return t
}

func (responsible *Responsible) collectOperations() error {
	for _, path := range responsible.spec.Paths.InMatchingOrder() {
		pathItem := responsible.spec.Paths.Value(path)
		if pathItem == nil {
			continue
		}
		for _, method := range supportedMethods {
			openAPIOperation := pathItem.GetOperation(method)
			if openAPIOperation == nil {
				continue
			}
			if openAPIOperation.OperationID == "" {
				return fmt.Errorf("%s %s has empty operationId", method, path)
			}
			if _, exists := responsible.operations[openAPIOperation.OperationID]; exists {
				return fmt.Errorf("duplicate operationId %q", openAPIOperation.OperationID)
			}

			route := &routers.Route{
				Spec:      responsible.spec,
				Path:      path,
				PathItem:  pathItem,
				Method:    method,
				Operation: openAPIOperation,
			}
			operation := &Operation{
				owner:     responsible,
				route:     route,
				method:    method,
				path:      path,
				operation: openAPIOperation,
			}
			responsible.operations[openAPIOperation.OperationID] = operation
			responsible.orderedOperations = append(responsible.orderedOperations, operation)
		}
	}
	return nil
}

func (responsible *Responsible) availableOperationIDs() string {
	operationIDs := make([]string, 0, len(responsible.orderedOperations))
	for _, operation := range responsible.orderedOperations {
		operationIDs = append(operationIDs, operation.operation.OperationID)
	}
	if len(operationIDs) == 0 {
		return "(none)"
	}
	return strings.Join(operationIDs, ", ")
}

func (responsible *Responsible) requestURL(path string, query url.Values) string {
	if responsible.baseURL == nil {
		return withQuery(path, query)
	}
	u := *responsible.baseURL
	u.Path = joinURLPath(u.Path, path)
	u.RawQuery = query.Encode()
	return u.String()
}

func (responsible *Responsible) responseValidationInput(
	req *http.Request,
	res *http.Response,
	body []byte,
) (*openapi3filter.ResponseValidationInput, error) {
	requestInput, err := responsible.requestValidationInput(req)
	if err != nil {
		return nil, err
	}
	options := responsible.validationOptions
	return &openapi3filter.ResponseValidationInput{
		RequestValidationInput: requestInput,
		Status:                 res.StatusCode,
		Header:                 res.Header,
		Body:                   io.NopCloser(bytes.NewReader(body)),
		Options:                &options,
	}, nil
}

func (responsible *Responsible) requestValidationInput(req *http.Request) (*openapi3filter.RequestValidationInput, error) {
	if meta, ok := req.Context().Value(requestMetadataKey{}).(requestMetadata); ok {
		return &openapi3filter.RequestValidationInput{
			Request:    req,
			PathParams: meta.pathParams,
			Route:      meta.route,
		}, nil
	}

	route, pathParams, err := responsible.router.FindRoute(req)
	if err != nil {
		return nil, fmt.Errorf("find openapi route: %w", err)
	}
	return &openapi3filter.RequestValidationInput{
		Request:    req,
		PathParams: pathParams,
		Route:      route,
	}, nil
}

func replacePathParams(path string, pathParams map[string]string) (string, error) {
	var missing []string
	out := pathParamRE.ReplaceAllStringFunc(path, func(token string) string {
		name := token[1 : len(token)-1]
		value, ok := pathParams[name]
		if !ok {
			missing = append(missing, name)
			return token
		}
		return value
	})
	if len(missing) > 0 {
		return "", fmt.Errorf("missing path params: %s", strings.Join(missing, ", "))
	}
	if strings.Contains(out, "{") || strings.Contains(out, "}") {
		return "", fmt.Errorf("unresolved path params in %q", out)
	}
	return out, nil
}

func requestBody(options RequestOptions) (io.Reader, string, error) {
	sources := 0
	if options.JSON != nil {
		sources++
	}
	if options.Text != nil {
		sources++
	}
	if options.Body != nil {
		sources++
	}
	if sources > 1 {
		return nil, "", errors.New("only one of JSON, Text, or Body can be set")
	}

	switch {
	case options.JSON != nil:
		data, err := json.Marshal(options.JSON)
		if err != nil {
			return nil, "", err
		}
		return bytes.NewReader(data), contentTypeOrDefault(options.ContentType, "application/json"), nil
	case options.Text != nil:
		return strings.NewReader(*options.Text), contentTypeOrDefault(options.ContentType, "text/plain; charset=utf-8"), nil
	case options.Body != nil:
		return options.Body, options.ContentType, nil
	default:
		return nil, "", nil
	}
}

func contentTypeOrDefault(contentType string, fallback string) string {
	if contentType != "" {
		return contentType
	}
	return fallback
}

func readAndReplaceBody(res *http.Response) ([]byte, error) {
	if res.Body == nil {
		return nil, nil
	}
	defer res.Body.Close()
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	res.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}

func withQuery(path string, query url.Values) string {
	if len(query) == 0 {
		return path
	}
	return path + "?" + query.Encode()
}

func joinURLPath(basePath string, path string) string {
	basePath = strings.TrimRight(basePath, "/")
	path = strings.TrimLeft(path, "/")
	if basePath == "" {
		return "/" + path
	}
	if path == "" {
		return basePath
	}
	return basePath + "/" + path
}

func cloneHeader(header http.Header) http.Header {
	if header == nil {
		return http.Header{}
	}
	return header.Clone()
}

func clonePathParams(pathParams map[string]string) map[string]string {
	if pathParams == nil {
		return nil
	}
	cloned := make(map[string]string, len(pathParams))
	for key, value := range pathParams {
		cloned[key] = value
	}
	return cloned
}

var supportedMethods = []string{
	http.MethodConnect,
	http.MethodDelete,
	http.MethodGet,
	http.MethodHead,
	http.MethodOptions,
	http.MethodPatch,
	http.MethodPost,
	http.MethodPut,
	http.MethodTrace,
}
