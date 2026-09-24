package write

import (
	"bytes"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"

	"connectrpc.com/connect"
)

// gatewayErrorClient accepts gateway errors whose code is an HTTP status number
// instead of a Connect code string. Only unary RPC error responses are adapted.
type gatewayErrorClient struct {
	connect.HTTPClient
}

const maxGatewayErrorSize = 64 * 1024

func (c gatewayErrorClient) Do(req *http.Request) (*http.Response, error) {
	resp, err := c.HTTPClient.Do(req)
	if err != nil || resp.StatusCode < 400 || resp.Body == nil {
		return resp, err
	}
	mediaType, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	if mediaType != "application/json" || resp.Header.Get("Content-Encoding") != "" {
		return resp, nil
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxGatewayErrorSize+1))
	if readErr != nil {
		_ = resp.Body.Close()
		return nil, readErr
	}
	// Preserve the original body and its Close method, including for errors that
	// cannot be normalized or are too large to inspect.
	resp.Body = &gatewayErrorBody{Reader: io.MultiReader(bytes.NewReader(body), resp.Body), Closer: resp.Body}
	if len(body) > maxGatewayErrorSize {
		return resp, nil
	}
	normalized := normalizeGatewayError(body)
	if normalized != nil {
		resp.Body = &gatewayErrorBody{Reader: bytes.NewReader(normalized), Closer: resp.Body}
		resp.ContentLength = int64(len(normalized))
		resp.Header.Set("Content-Length", strconv.Itoa(len(normalized)))
	}
	return resp, nil
}

type gatewayErrorBody struct {
	io.Reader
	io.Closer
}

func normalizeGatewayError(body []byte) []byte {
	var fields map[string]json.RawMessage
	if json.Unmarshal(body, &fields) != nil {
		return nil
	}
	var status int
	if json.Unmarshal(fields["code"], &status) != nil || status < 400 || status > 599 {
		return nil
	}
	var errorType string
	_ = json.Unmarshal(fields["type"], &errorType)
	var code connect.Code
	if code.UnmarshalText([]byte(strings.ToLower(errorType))) != nil {
		code = gatewayStatusCode(status)
	}
	fields["code"], _ = json.Marshal(code.String())
	normalized, _ := json.Marshal(fields)
	return normalized
}

func gatewayStatusCode(status int) connect.Code {
	switch status {
	case http.StatusBadRequest:
		return connect.CodeInvalidArgument
	case http.StatusUnauthorized:
		return connect.CodeUnauthenticated
	case http.StatusForbidden:
		return connect.CodePermissionDenied
	case http.StatusNotFound:
		return connect.CodeNotFound
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return connect.CodeDeadlineExceeded
	case http.StatusConflict:
		return connect.CodeAborted
	case http.StatusTooManyRequests:
		return connect.CodeResourceExhausted
	case http.StatusInternalServerError:
		return connect.CodeInternal
	case http.StatusNotImplemented:
		return connect.CodeUnimplemented
	case http.StatusBadGateway, http.StatusServiceUnavailable:
		return connect.CodeUnavailable
	default:
		return connect.CodeUnknown
	}
}
