package write

import (
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
		{"connect", `{"code":"deadline_exceeded","message":"deadline expired"}`, connect.CodeDeadlineExceeded, true},
		{"without type", `{"code":504,"message":"deadline expired"}`, connect.CodeDeadlineExceeded, true},
		{"unknown type", `{"code":504,"message":"deadline expired","type":"OTHER"}`, connect.CodeDeadlineExceeded, true},
		{"type takes precedence", `{"code":500,"message":"deadline expired","type":"INVALID_ARGUMENT"}`, connect.CodeInvalidArgument, false},
		{"rate limit", `{"code":429,"message":"deadline expired"}`, connect.CodeResourceExhausted, true},
		{"forbidden", `{"code":403,"message":"deadline expired"}`, connect.CodePermissionDenied, false},
		{"unknown status", `{"code":599,"message":"deadline expired"}`, connect.CodeUnknown, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json; charset=utf-8")
				w.Header().Set("X-Request-Id", "request-id")
				w.WriteHeader(http.StatusGatewayTimeout)
				_, _ = io.WriteString(w, tc.body)
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

func TestGatewayErrorClientPassThrough(t *testing.T) {
	for _, tc := range []struct {
		name        string
		status      int
		contentType string
		body        string
	}{
		{"success", 200, "application/json", `{"code":504}`},
		{"html", 504, "text/html", "<html>gateway timeout</html>"},
		{"malformed", 504, "application/json", `{"code":504`},
		{"string code", 504, "application/json", `{"code":"deadline_exceeded","message":"timeout"}`},
		{"missing code", 504, "application/json", `{"message":"timeout"}`},
		{"null code", 504, "application/json", `{"code":null}`},
		{"oversized", 504, "application/json", `{"code":504,"message":"` + strings.Repeat("x", maxGatewayErrorSize) + `"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			req, err := http.NewRequest(http.MethodPost, server.URL, nil)
			require.NoError(t, err)
			resp, err := (gatewayErrorClient{server.Client()}).Do(req)
			require.NoError(t, err)
			defer resp.Body.Close()
			body, err := io.ReadAll(resp.Body)
			require.NoError(t, err)
			require.Equal(t, tc.body, string(body))
			require.Equal(t, tc.status, resp.StatusCode)
		})
	}
}
