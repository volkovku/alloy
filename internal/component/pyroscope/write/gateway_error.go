package write

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
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
	encoding := resp.Header.Get("Content-Encoding")
	if encoding != "" && encoding != "identity" && encoding != "gzip" {
		return nil, gatewayResponseError(resp, nil, "unsupported content encoding")
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, maxGatewayErrorSize+1))
	if readErr != nil {
		_ = resp.Body.Close()
		return nil, readErr
	}
	if len(body) > maxGatewayErrorSize {
		if encoding == "gzip" {
			body = nil // Do not log compressed binary data.
		}
		return nil, gatewayResponseError(resp, body, "encoded error body exceeds 64 KiB")
	}
	if encoding == "gzip" {
		// Connect sets Accept-Encoding itself, so net/http does not decompress
		// these responses. Bound both compressed and decompressed input sizes.
		reader, err := gzip.NewReader(bytes.NewReader(body))
		if err != nil {
			return nil, gatewayResponseError(resp, nil, "invalid gzip error body")
		}
		body, err = io.ReadAll(io.LimitReader(reader, maxGatewayErrorSize+1))
		_ = reader.Close()
		if err != nil {
			return nil, gatewayResponseError(resp, body, "invalid gzip error body")
		}
		if len(body) > maxGatewayErrorSize {
			return nil, gatewayResponseError(resp, body, "decoded error body exceeds 64 KiB")
		}
	}
	normalized := normalizeGatewayError(body)
	if normalized != nil {
		resp.Body = &gatewayErrorBody{Reader: bytes.NewReader(normalized), Closer: resp.Body}
		resp.ContentLength = int64(len(normalized))
		resp.Header.Set("Content-Length", strconv.Itoa(len(normalized)))
		resp.Header.Set("Content-Type", "application/json")
		resp.Header.Del("Content-Encoding")
		resp.Uncompressed = encoding == "gzip"
	} else {
		return nil, gatewayResponseError(resp, body, "unrecognized error body")
	}
	return resp, nil
}

// Preserve evidence from proxy and malformed responses that Connect would
// otherwise replace with just the HTTP status. Limit the excerpt in logs.
func gatewayResponseError(resp *http.Response, body []byte, reason string) error {
	_ = resp.Body.Close()
	const maxExcerpt = 4096
	excerpt := string(body)
	if len(body) > maxExcerpt {
		excerpt = string(body[:maxExcerpt]) + " [truncated]"
	}
	err := connect.NewError(gatewayStatusCode(resp.StatusCode), fmt.Errorf(
		"HTTP %s: %s (content-type=%q, content-encoding=%q): %s",
		resp.Status, reason, resp.Header.Get("Content-Type"), resp.Header.Get("Content-Encoding"), excerpt,
	))
	for key, values := range resp.Header {
		err.Meta()[key] = append([]string(nil), values...)
	}
	return err
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
	var message string
	if raw, ok := fields["message"]; ok {
		if json.Unmarshal(raw, &message) != nil {
			return nil
		}
		if rawCode, ok := fields["code"]; !ok || bytes.Equal(rawCode, []byte("null")) {
			return body // Connect infers a missing code from the HTTP status.
		}
	}
	var stringCode string
	if json.Unmarshal(fields["code"], &stringCode) == nil && stringCode != "" {
		return body
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
