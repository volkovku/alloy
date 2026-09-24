package write

import (
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	pushv1 "github.com/grafana/pyroscope/api/gen/proto/go/push/v1"
	"github.com/grafana/pyroscope/api/gen/proto/go/push/v1/pushv1connect"
	"github.com/stretchr/testify/require"
)

func TestGatewayErrorClientConnect(t *testing.T) {
	for _, tc := range []struct {
		name  string
		body  string
		code  connect.Code
		retry bool
	}{
		{"gateway", `{"code":504,"message":"deadline expired","type":"DEADLINE_EXCEEDED"}`, connect.CodeDeadlineExceeded, true},
		{"missing code", `{"message":"deadline expired"}`, connect.CodeUnavailable, true},
		{"null code", `{"code":null,"message":"deadline expired"}`, connect.CodeUnavailable, true},
		{"connect", `{"code":"deadline_exceeded","message":"deadline expired"}`, connect.CodeDeadlineExceeded, true},
		{"without type", `{"code":504,"message":"deadline expired"}`, connect.CodeDeadlineExceeded, true},
		{"unknown type", `{"code":504,"message":"deadline expired","type":"OTHER"}`, connect.CodeDeadlineExceeded, true},
		{"type takes precedence", `{"code":500,"message":"deadline expired","type":"INVALID_ARGUMENT"}`, connect.CodeInvalidArgument, false},
		{"rate limit", `{"code":429,"message":"deadline expired"}`, connect.CodeResourceExhausted, true},
		{"forbidden", `{"code":403,"message":"deadline expired"}`, connect.CodePermissionDenied, false},
		{"unknown status", `{"code":599,"message":"deadline expired"}`, connect.CodeUnknown, true},
	} {
		for _, contentType := range []string{"application/json", "application/json; charset=UTF-8", "text/plain", ""} {
			for _, encoding := range []string{"", "identity", "gzip"} {
				t.Run(tc.name+"/"+contentType+"/"+encoding, func(t *testing.T) {
					server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.Header()["Content-Type"] = []string{contentType}
						w.Header().Set("X-Request-Id", "request-id")
						w.Header().Set("Content-Encoding", encoding)
						w.WriteHeader(http.StatusGatewayTimeout)
						var writer io.Writer = w
						if encoding == "gzip" {
							gz := gzip.NewWriter(w)
							defer gz.Close()
							writer = gz
						}
						_, _ = io.WriteString(writer, tc.body)
					}))
					defer server.Close()
					client := pushv1connect.NewPusherServiceClient(gatewayErrorClient{server.Client()}, server.URL)
					_, err := client.Push(context.Background(), connect.NewRequest(&pushv1.PushRequest{}))
					var connectErr *connect.Error
					require.ErrorAs(t, err, &connectErr)
					require.Equal(t, tc.code, connectErr.Code())
					require.Equal(t, "deadline expired", connectErr.Message())
					require.Equal(t, "request-id", connectErr.Meta().Get("X-Request-Id"))
					require.True(t, connect.IsWireError(err))
					require.Equal(t, tc.retry, shouldRetry(err, true))
					if tc.code == connect.CodeResourceExhausted {
						require.False(t, shouldRetry(err, false))
					}
				})
			}
		}

	}

}

func TestGatewayErrorClientUnexpectedResponse(t *testing.T) {
	for _, tc := range []struct {
		name, contentType, body, want string
	}{
		{"html", "text/html", "<html>upstream timeout: request-id=123</html>", "upstream timeout: request-id=123"},
		{"plain text", "text/plain", "upstream took too long", "upstream took too long"},
		{"malformed JSON", "application/json", `{"code":504`, `{"code":504`},
		{"empty body", "application/json", "", "unrecognized error body"},
		{"oversized", "application/json", strings.Repeat("x", maxGatewayErrorSize+1), "exceeds 64 KiB"},
	} {
		for _, encoding := range []string{"", "gzip"} {
			t.Run(tc.name+"/"+encoding, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", tc.contentType)
					w.Header().Set("Content-Encoding", encoding)
					w.Header().Set("X-Request-Id", "request-id")
					w.WriteHeader(http.StatusGatewayTimeout)
					var writer io.Writer = w
					if encoding == "gzip" {
						gz := gzip.NewWriter(w)
						defer gz.Close()
						writer = gz
					}
					_, _ = io.WriteString(writer, tc.body)
				}))
				defer server.Close()
				client := pushv1connect.NewPusherServiceClient(gatewayErrorClient{server.Client()}, server.URL)
				_, err := client.Push(context.Background(), connect.NewRequest(&pushv1.PushRequest{}))
				require.ErrorContains(t, err, "504 Gateway Timeout")
				require.ErrorContains(t, err, tc.want)
				require.ErrorContains(t, err, "content-type=")
				require.Less(t, len(err.Error()), 4600)
				require.True(t, shouldRetry(err, true))
				var connectErr *connect.Error
				require.ErrorAs(t, err, &connectErr)
				require.Equal(t, "request-id", connectErr.Meta().Get("X-Request-Id"))
			})
		}
	}
}

func TestGatewayErrorClientSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":504}`)
	}))
	defer server.Close()
	req, err := http.NewRequest(http.MethodPost, server.URL, nil)
	require.NoError(t, err)
	resp, err := (gatewayErrorClient{server.Client()}).Do(req)
	require.NoError(t, err)
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	require.Equal(t, `{"code":504}`, string(body))
}
