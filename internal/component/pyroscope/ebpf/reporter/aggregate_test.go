//go:build unix

package reporter

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/google/pprof/profile"
	"github.com/grafana/alloy/internal/component/pyroscope/ebpf/discovery"
	"github.com/grafana/alloy/internal/component/pyroscope/ebpf/reporter/args"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/stretchr/testify/require"
	"go.opentelemetry.io/ebpf-profiler/libpf"
	"go.opentelemetry.io/ebpf-profiler/reporter/samples"
)

func aggregateTestProfile(start uint64, buildID string, value int64, sampleLabels map[string][]string) *profile.Profile {
	m := &profile.Mapping{ID: 1, Start: start, Limit: start + 4096, File: "/app", BuildID: buildID, HasFunctions: true}
	f := &profile.Function{ID: 1, Name: "work"}
	l := &profile.Location{ID: 1, Mapping: m, Address: start + 16, Line: []profile.Line{{Function: f}}}
	return &profile.Profile{
		SampleType: []*profile.ValueType{{Type: "cpu", Unit: "nanoseconds"}},
		PeriodType: &profile.ValueType{Type: "cpu", Unit: "nanoseconds"}, Period: 100,
		TimeNanos: 1000,
		Mapping:   []*profile.Mapping{m}, Function: []*profile.Function{f}, Location: []*profile.Location{l},
		Sample: []*profile.Sample{{Location: []*profile.Location{l}, Value: []int64{value}, Label: sampleLabels}},
	}
}

func TestAggregateProfiles(t *testing.T) {
	for _, tc := range []struct {
		name    string
		buildID string
		label   map[string][]string
		samples int
	}{
		{name: "same stack across ASLR", buildID: "build", samples: 1},
		{name: "different binary", buildID: "other", samples: 2},
		{name: "different sample labels", buildID: "build", label: map[string][]string{"span_id": {"span"}}, samples: 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rep := newReporter()
			lbs := labels.FromStrings("service_name", "service")
			a := aggregateTestProfile(0x1000, "build", 10, nil)
			b := aggregateTestProfile(0x9000, tc.buildID, 20, tc.label)
			groups := make(profileGroups)
			groups.add([]builtProfile{{profile: a, labels: lbs}, {profile: b, labels: lbs}}, "samples")
			result := rep.encodeGroups(groups, args.AggregationOptions{})
			require.Len(t, result, 1)
			require.Equal(t, lbs, result[0].Labels)
			merged, err := profile.ParseData(result[0].Raw)
			require.NoError(t, err)
			require.NoError(t, merged.CheckValid())
			require.Len(t, merged.Sample, tc.samples)
			var total int64
			for _, sample := range merged.Sample {
				total += sample.Value[0]
			}
			require.EqualValues(t, 30, total)
			require.EqualValues(t, 100, merged.Period)
			require.EqualValues(t, 1000, merged.TimeNanos)
			require.EqualValues(t, 10, a.Sample[0].Value[0])
			require.EqualValues(t, 20, b.Sample[0].Value[0])
		})
	}
}

func TestAggregateSingleProfile(t *testing.T) {
	rep := newReporter()
	p := aggregateTestProfile(0x1000, "build", 10, nil)
	p.Sample = append(p.Sample, &profile.Sample{Location: p.Sample[0].Location, Value: []int64{20}})
	groups := make(profileGroups)
	groups.add([]builtProfile{{profile: p, labels: labels.FromStrings("service_name", "service")}}, "samples")
	result := rep.encodeGroups(groups, args.AggregationOptions{})
	require.Len(t, result, 1)
	merged, err := profile.ParseData(result[0].Raw)
	require.NoError(t, err)
	require.Len(t, merged.Sample, 1)
	require.EqualValues(t, 30, merged.Sample[0].Value[0])
}

func reportTestEvents(count int) samples.TraceEventsTree {
	tree := make(samples.TraceEventsTree, count)
	for i := 0; i < count; i++ {
		tree[samples.ResourceKey{PID: int64(i + 1)}] = samples.ResourceToProfiles{
			Events: map[*samples.TypeMetadata]samples.SampleToEvents{
				profileTypeSampling: {
					{}: &samples.TraceEvents{Frames: singleFrameTrace(libpf.PythonFrame, libpf.FrameMappingFile{}, 16, "work", "app.py", 1), Timestamps: []uint64{1, 2}},
				},
			},
		}
	}
	return tree
}

func TestReportAggregatedProfiles(t *testing.T) {
	rep := newReporterWithTargets([]discovery.DiscoveredTarget{
		{"__process_pid__": "1", "service_name": "service"},
		{"__process_pid__": "2", "service_name": "service"},
	})
	tree := reportTestEvents(2)
	for _, aggregate := range []bool{false, true, false} {
		rep.UpdateProfileOptions(false, aggregate, args.AggregationOptions{})
		events := rep.traceEvents.WLock()
		*events = tree
		rep.traceEvents.WUnlock(&events)
		var received []PPROF
		rep.consumer = func(_ context.Context, profiles []PPROF) { received = profiles }
		start := rep.intervalStart
		rep.reportProfile(t.Context())
		duration := rep.intervalStart.Sub(start).Nanoseconds()
		if aggregate {
			require.Len(t, received, 1)
		} else {
			require.Len(t, received, 2)
		}
		var total int64
		for _, raw := range received {
			p, err := profile.ParseData(raw.Raw)
			require.NoError(t, err)
			require.Len(t, p.Sample, 1)
			require.Empty(t, p.Sample[0].Label["pid"])
			require.Equal(t, start.UnixNano(), p.TimeNanos)
			require.Equal(t, duration, p.DurationNanos)
			total += p.Sample[0].Value[0]
		}
		require.EqualValues(t, 4*(int64(1e9)/97), total)
	}
	// A subsequent empty interval must not retain the previous aggregate.
	rep.UpdateProfileOptions(false, true, args.AggregationOptions{})
	rep.consumer = func(_ context.Context, profiles []PPROF) { require.Empty(t, profiles) }
	rep.reportProfile(t.Context())
}

func TestAggregateGroups(t *testing.T) {
	rep := newReporter()
	groups := make(profileGroups)
	for _, service := range []string{"one", "two"} {
		for _, sampleType := range []string{"samples", "events", "off_cpu"} {
			p := aggregateTestProfile(0x1000, "build", 10, nil)
			p.SampleType[0].Type = sampleType
			groups.add([]builtProfile{{profile: p, labels: labels.FromStrings("service_name", service)}}, sampleType)
		}
	}
	require.Len(t, rep.encodeGroups(groups, args.AggregationOptions{}), 6)
}

func BenchmarkReportProfiles(b *testing.B) {
	for _, count := range []int{1, 100, 1000} {
		for _, aggregate := range []bool{false, true} {
			b.Run(fmt.Sprintf("processes=%d/aggregate=%t", count, aggregate), func(b *testing.B) {
				targets := make([]discovery.DiscoveredTarget, count)
				for i := range targets {
					targets[i] = discovery.DiscoveredTarget{"__process_pid__": fmt.Sprint(i + 1), "service_name": "service"}
				}
				rep := newReporterWithTargets(targets)
				rep.UpdateProfileOptions(false, aggregate, args.AggregationOptions{})
				tree := reportTestEvents(count)
				var size int
				rep.consumer = func(_ context.Context, profiles []PPROF) {
					size = 0
					for _, p := range profiles {
						size += len(p.Raw)
					}
				}
				b.ReportAllocs()
				for b.Loop() {
					events := rep.traceEvents.WLock()
					*events = tree
					rep.traceEvents.WUnlock(&events)
					rep.reportProfile(b.Context())
				}
				b.ReportMetric(float64(size), "pprof-bytes/op")
			})
		}
	}
}

func TestAggregateFailurePreservesProfiles(t *testing.T) {
	rep := newReporter()
	a := aggregateTestProfile(0x1000, "build", 10, nil)
	b := aggregateTestProfile(0x2000, "build", 20, nil)
	b.SampleType[0].Type = "incompatible"
	lbs := labels.FromStrings("service_name", "service")
	groups := make(profileGroups)
	groups.add([]builtProfile{{profile: a, labels: lbs}, {profile: b, labels: lbs}}, "samples")
	result := rep.encodeGroups(groups, args.AggregationOptions{})
	require.Len(t, result, 2)
	for i, raw := range result {
		p, err := profile.ParseData(raw.Raw)
		require.NoError(t, err)
		require.EqualValues(t, (i+1)*10, p.Sample[0].Value[0])
	}
}

func TestProfileOptionsConcurrentUpdate(t *testing.T) {
	rep := newReporter()
	var wg sync.WaitGroup
	wg.Go(func() {
		for range 1000 {
			rep.UpdateProfileOptions(true, false, args.AggregationOptions{})
			rep.UpdateProfileOptions(false, true, args.AggregationOptions{MaxStackDepth: 42})
		}
	})
	for range 1000 {
		pid, aggregate, options := rep.profileOptions()
		require.False(t, pid && aggregate)
		if aggregate {
			require.Equal(t, 42, options.MaxStackDepth)
		}
	}
	wg.Wait()
}

func TestReportAggregatedProfileTypes(t *testing.T) {
	rep := newReporterWithTargets([]discovery.DiscoveredTarget{
		{"__process_pid__": "1", "service_name": "service"},
		{"__process_pid__": "2", "service_name": "service"},
	})
	rep.UpdateProfileOptions(false, true, args.AggregationOptions{})
	tree := reportTestEvents(2)
	for key, resource := range tree {
		events := resource.Events[profileTypeSampling]
		events[samples.SampleKey{}].Values = []int64{10, 20}
		resource.Events[profileTypeOffCPU] = events
		resource.Events[profileTypeProbe] = events
		tree[key] = resource
	}
	events := rep.traceEvents.WLock()
	*events = tree
	rep.traceEvents.WUnlock(&events)
	rep.consumer = func(_ context.Context, profiles []PPROF) {
		require.Len(t, profiles, 3)
		totals := map[string]int64{}
		for _, raw := range profiles {
			p, err := profile.ParseData(raw.Raw)
			require.NoError(t, err)
			require.Len(t, p.Sample, 1)
			totals[p.SampleType[0].Type] = p.Sample[0].Value[0]
		}
		require.Equal(t, map[string]int64{"cpu": 4 * (int64(1e9) / 97), "offcpu": 60, "uprobe": 4}, totals)
	}
	rep.reportProfile(t.Context())
}

func TestAggregatePostprocessing(t *testing.T) {
	for _, tc := range []struct {
		name    string
		options args.AggregationOptions
		want    map[string]int64
	}{
		{"disabled", args.AggregationOptions{}, map[string]int64{"axr": 3, "bxr": 4, "xr": 2, "yr": 91}},
		{"depth preserves parent weight", args.AggregationOptions{MaxStackDepth: 2}, map[string]int64{"xr": 9, "yr": 91}},
		{"root only", args.AggregationOptions{MaxStackDepth: 1}, map[string]int64{"r": 100}},
		{"percent after depth merge", args.AggregationOptions{MaxStackDepth: 2, MinSamplePercent: 9}, map[string]int64{"xr": 9, "yr": 91}},
		{"percent removes smaller", args.AggregationOptions{MaxStackDepth: 2, MinSamplePercent: 10}, map[string]int64{"yr": 91}},
		{"absolute equality retained", args.AggregationOptions{MinSampleValue: 4}, map[string]int64{"bxr": 4, "yr": 91}},
		{"both thresholds", args.AggregationOptions{MaxStackDepth: 2, MinSamplePercent: 5, MinSampleValue: 10}, map[string]int64{"yr": 91}},
		{"all removed", args.AggregationOptions{MinSampleValue: 101}, map[string]int64{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &profile.Profile{SampleType: []*profile.ValueType{{Type: "cpu", Unit: "nanoseconds"}}, PeriodType: &profile.ValueType{}}
			locations := map[rune]*profile.Location{}
			for i, name := range "abxyr" {
				f := &profile.Function{ID: uint64(i + 1), Name: string(name)}
				l := &profile.Location{ID: uint64(i + 1), Line: []profile.Line{{Function: f}}}
				p.Function = append(p.Function, f)
				p.Location = append(p.Location, l)
				locations[name] = l
			}
			for stack, value := range map[string]int64{"axr": 3, "bxr": 4, "xr": 2, "yr": 91} {
				sample := &profile.Sample{Value: []int64{value}}
				for _, name := range stack {
					sample.Location = append(sample.Location, locations[name])
				}
				p.Sample = append(p.Sample, sample)
			}
			groups := make(profileGroups)
			groups.add([]builtProfile{{profile: p}}, "samples")
			raw := newReporter().encodeGroups(groups, tc.options)
			require.Len(t, raw, 1)
			got, err := profile.ParseData(raw[0].Raw)
			require.NoError(t, err)
			require.NoError(t, got.CheckValid())
			values := map[string]int64{}
			for _, sample := range got.Sample {
				var stack string
				for _, l := range sample.Location {
					stack += l.Line[0].Function.Name
				}
				values[stack] = sample.Value[0]
			}
			require.Equal(t, tc.want, values)
			require.Len(t, p.Sample, 4, "input profiles must remain unchanged")
		})
	}
}

func TestAggregateThresholdPreservesLabels(t *testing.T) {
	p := aggregateTestProfile(0x1000, "build", 4, map[string][]string{"span_id": {"one"}})
	p.Sample = append(p.Sample, &profile.Sample{Location: p.Sample[0].Location, Value: []int64{6}, Label: map[string][]string{"span_id": {"two"}}})
	got := postprocessAggregate(p, args.AggregationOptions{MaxStackDepth: 1, MinSamplePercent: 50})
	require.Len(t, got.Sample, 1)
	require.Equal(t, []string{"two"}, got.Sample[0].Label["span_id"])
	require.EqualValues(t, 6, got.Sample[0].Value[0])
}

func TestAggregatePercentBoundary(t *testing.T) {
	p := aggregateTestProfile(0x1000, "build", 7, nil)
	p.Sample = append(p.Sample, &profile.Sample{Location: p.Sample[0].Location, Value: []int64{93}, Label: map[string][]string{"span_id": {"other"}}})
	got := postprocessAggregate(p, args.AggregationOptions{MinSamplePercent: 7})
	require.Len(t, got.Sample, 2)
}

func TestReportAggregationLimitsUpdate(t *testing.T) {
	rep := newReporterWithTargets([]discovery.DiscoveredTarget{{"__process_pid__": "1", "service_name": "service"}})
	for _, tc := range []struct {
		aggregate bool
		minimum   int64
		want      int
	}{
		{true, 1e9, 0}, {true, 0, 1}, {false, 1e9, 1},
	} {
		rep.UpdateProfileOptions(false, tc.aggregate, args.AggregationOptions{MinSampleValue: tc.minimum})
		events := rep.traceEvents.WLock()
		*events = reportTestEvents(1)
		rep.traceEvents.WUnlock(&events)
		rep.consumer = func(_ context.Context, profiles []PPROF) {
			require.Len(t, profiles, 1)
			p, err := profile.ParseData(profiles[0].Raw)
			require.NoError(t, err)
			require.Len(t, p.Sample, tc.want)
		}
		rep.reportProfile(t.Context())
	}
}
