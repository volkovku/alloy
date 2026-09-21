package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"

	"connectrpc.com/connect"
	pushv1 "github.com/grafana/pyroscope/api/gen/proto/go/push/v1"
	"github.com/grafana/pyroscope/api/gen/proto/go/push/v1/pushv1connect"
)

type server struct {
	sequence atomic.Uint64
	output   string
}

func (s *server) Push(
	_ context.Context,
	req *connect.Request[pushv1.PushRequest],
) (*connect.Response[pushv1.PushResponse], error) {
	if err := os.MkdirAll(s.output, 0o755); err != nil {
		return nil, err
	}
	for _, series := range req.Msg.Series {
		labels, err := json.Marshal(series.Labels)
		if err != nil {
			return nil, err
		}
		for _, sample := range series.Samples {
			id := s.sequence.Add(1)
			base := filepath.Join(s.output, fmt.Sprintf("%03d", id))
			if err := os.WriteFile(base+".labels.json", labels, 0o644); err != nil {
				return nil, err
			}
			if err := os.WriteFile(base+".pprof", sample.RawProfile, 0o644); err != nil {
				return nil, err
			}
			fmt.Printf("captured %s.pprof bytes=%d\n", base, len(sample.RawProfile))
		}
	}
	return connect.NewResponse(&pushv1.PushResponse{}), nil
}

func main() {
	output := flag.String("output", "profiles", "Directory for captured pprof profiles")
	listen := flag.String("listen", "127.0.0.1:14040", "HTTP listen address")
	flag.Parse()
	s := &server{output: *output}
	_, handler := pushv1connect.NewPusherServiceHandler(s)
	if err := http.ListenAndServe(*listen, handler); err != nil {
		panic(err)
	}
}
