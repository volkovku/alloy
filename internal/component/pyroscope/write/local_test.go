package write

import (
	"context"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"connectrpc.com/connect"
	"github.com/grafana/alloy/internal/component/pyroscope"
	pyrotestlogger "github.com/grafana/alloy/internal/component/pyroscope/util/testlog"
	"github.com/grafana/alloy/syntax"
	pushv1 "github.com/grafana/pyroscope/api/gen/proto/go/push/v1"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/otel/trace/noop"
)

func TestLocalProfiles(t *testing.T) {
	for _, mode := range []string{"enabled", "disabled", "write failure"} {
		t.Run(mode, func(t *testing.T) {
			directory := filepath.Join(t.TempDir(), "profiles")
			cfg := DefaultArguments()
			if mode != "disabled" {
				cfg.ProfileDirectory = directory
			}
			if mode == "write failure" {
				require.NoError(t, os.WriteFile(directory, []byte("occupied"), 0o600))
			}
			f, err := newFanOut(pyrotestlogger.TestLogger(t), noop.Tracer{}, cfg, newMetrics(prometheus.NewRegistry()), "test", "test", t.TempDir())
			require.NoError(t, err)
			// Two endpoints each retry once. Local writes must not multiply with sends.
			for i := 0; i < 2; i++ {
				opts := GetDefaultEndpointOptions()
				opts.MinBackoff = 1
				opts.MaxBackoff = 1
				var mu sync.Mutex
				attempts := 0
				f.endpoints = append(f.endpoints, &endpointClient{options: &opts, pushClient: PushFunc(func(_ context.Context, _ *connect.Request[pushv1.PushRequest]) (*connect.Response[pushv1.PushResponse], error) {
					mu.Lock()
					defer mu.Unlock()
					attempts++
					if attempts == 1 {
						return nil, connect.NewError(connect.CodeUnavailable, nil)
					}
					return connect.NewResponse(&pushv1.PushResponse{}), nil
				})})
			}
			require.NoError(t, f.Append(context.Background(), labels.FromStrings("service_name", "test"), []*pyroscope.RawSample{{RawProfile: []byte("profile one")}, {RawProfile: []byte("profile two")}}))
			// Exercise ingest with no remote endpoints, including local-only operation.
			f.endpoints = nil
			u, err := url.Parse("http://localhost/ingest?name=test")
			require.NoError(t, err)
			require.NoError(t, f.AppendIngest(context.Background(), &pyroscope.IncomingProfile{URL: u, Labels: labels.FromStrings("service_name", "test"), RawBody: []byte("ingest body")}))
			if mode == "disabled" {
				_, err := os.Stat(directory)
				require.True(t, os.IsNotExist(err))
				return
			}
			if mode == "write failure" {
				data, err := os.ReadFile(directory)
				require.NoError(t, err)
				require.Equal(t, "occupied", string(data))
				return
			}
			files, err := os.ReadDir(directory)
			require.NoError(t, err)
			require.Len(t, files, 3)
			contents := map[string]string{}
			for _, file := range files {
				data, err := os.ReadFile(filepath.Join(directory, file.Name()))
				require.NoError(t, err)
				contents[string(data)] = filepath.Ext(file.Name())
				info, err := file.Info()
				require.NoError(t, err)
				require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
			}
			require.Equal(t, map[string]string{"profile one": ".pprof", "profile two": ".pprof", "ingest body": ".ingest"}, contents)
		})
	}
}

func TestWriteProfileConcurrent(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "profiles")
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); require.NoError(t, writeProfile(directory, []byte("profile"), ".pprof")) }()
	}
	wg.Wait()
	files, err := os.ReadDir(directory)
	require.NoError(t, err)
	require.Len(t, files, 20)
}

func TestProfileDirectoryConfig(t *testing.T) {
	var args Arguments
	require.NoError(t, syntax.Unmarshal([]byte(`profile_directory = "profiles"`), &args))
	require.Equal(t, "profiles", args.ProfileDirectory)
	args.SetToDefault()
	require.Empty(t, args.ProfileDirectory)
}
