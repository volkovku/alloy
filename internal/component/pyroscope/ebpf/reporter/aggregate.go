//go:build unix

package reporter

import (
	"bytes"

	"github.com/google/pprof/profile"
	"github.com/grafana/alloy/internal/component/pyroscope/ebpf/reporter/args"
	"github.com/prometheus/prometheus/model/labels"
)

type builtProfile struct {
	profile *profile.Profile
	labels  labels.Labels
}

type profileGroupKey struct {
	labels     string
	sampleType string
}

type profileGroup struct {
	labels   labels.Labels
	profiles []*profile.Profile
}

type profileGroups map[profileGroupKey]*profileGroup

func (g profileGroups) add(profiles []builtProfile, sampleType string) {
	for _, p := range profiles {
		// A merged profile has no single process ID. Do not let discovery's
		// internal PID label prevent aggregation when pid_label is disabled.
		if p.labels.Has("__process_pid__") {
			builder := labels.NewBuilder(p.labels)
			builder.Del("__process_pid__")
			p.labels = builder.Labels()
		}
		// Use the full label set rather than a hash to avoid merging hash collisions.
		key := profileGroupKey{labels: p.labels.String(), sampleType: sampleType}
		group := g[key]
		if group == nil {
			group = &profileGroup{labels: p.labels}
			g[key] = group
		}
		group.profiles = append(group.profiles, p.profile)
	}
}

func (p *PPROFReporter) encodeGroups(groups profileGroups, options args.AggregationOptions) []PPROF {
	result := make([]PPROF, 0, len(groups))
	for key, group := range groups {
		// Merge once per group, not repeatedly into a growing accumulator.
		// Merge also normalizes ASLR addresses and sums samples with identical
		// stacks and sample labels, including duplicates within a single profile.
		// Off-CPU builders omit PeriodType. Merge expects a non-nil value
		// (as provided by profile.Parse), so normalize only the headers.
		for i, src := range group.profiles {
			if src.PeriodType == nil {
				header := *src
				header.PeriodType = &profile.ValueType{}
				group.profiles[i] = &header
			}
		}
		merged, err := profile.Merge(group.profiles)
		if err != nil {
			p.log.Error("failed to aggregate profiles", "err", err)
			// Preserve the original profiles if aggregation fails.
			for _, original := range group.profiles {
				result = append(result, p.encodeProfiles([]builtProfile{{profile: original, labels: group.labels}})...)
			}
		} else {
			// All profiles in a group cover the same collection window. Merge sums
			// durations, so restore the window duration before encoding.
			merged.DurationNanos = group.profiles[0].DurationNanos
			merged = postprocessAggregate(merged, options)
			// Release the input profiles before encoding the aggregate.
			clear(group.profiles)
			result = append(result, p.encodeProfiles([]builtProfile{{profile: merged, labels: group.labels}})...)
		}
		delete(groups, key)
	}
	return result
}

func (p *PPROFReporter) encodeProfiles(profiles []builtProfile) []PPROF {
	result := make([]PPROF, 0, len(profiles))
	for _, built := range profiles {
		var buf bytes.Buffer
		if _, err := writeProfile(&buf, built.profile); err != nil {
			p.log.Error("failed to encode profile", "err", err)
			continue
		}
		result = append(result, PPROF{Raw: buf.Bytes(), Labels: built.labels})
	}
	return result
}

// postprocessAggregate operates on the owned result of profile.Merge. Reporter
// profiles have one sample value: CPU/off-CPU nanoseconds or probe event count.
func postprocessAggregate(p *profile.Profile, options args.AggregationOptions) *profile.Profile {
	if options.MaxStackDepth > 0 {
		for _, sample := range p.Sample {
			if len(sample.Location) > options.MaxStackDepth {
				// pprof stores locations leaf first. Keep the root and its descendants
				// up to the limit, transferring all weight to the retained stack.
				sample.Location = sample.Location[len(sample.Location)-options.MaxStackDepth:]
			}
		}
		// Truncation can make previously distinct stacks identical. Sum them
		// before applying thresholds, preserving sample labels.
		p = p.Compact()
	}
	if options.MinSamplePercent == 0 && options.MinSampleValue == 0 {
		return p
	}
	var total float64
	for _, sample := range p.Sample {
		total += float64(sample.Value[0])
	}
	threshold := total * options.MinSamplePercent
	kept := p.Sample[:0]
	for _, sample := range p.Sample {
		if sample.Value[0] >= options.MinSampleValue && float64(sample.Value[0])*100 >= threshold {
			kept = append(kept, sample)
		}
	}
	clear(p.Sample[len(kept):])
	p.Sample = kept
	return p.Compact()
}

// UpdateProfileOptions applies options atomically for the next collection.
func (p *PPROFReporter) UpdateProfileOptions(pidLabel, aggregate bool, options args.AggregationOptions) {
	p.profileOptionsMut.Lock()
	defer p.profileOptionsMut.Unlock()
	p.pidLabel = pidLabel
	p.aggregateProfiles = aggregate
	p.aggregationOptions = options
}

func (p *PPROFReporter) profileOptions() (pidLabel, aggregate bool, options args.AggregationOptions) {
	p.profileOptionsMut.RLock()
	defer p.profileOptionsMut.RUnlock()
	return p.pidLabel, p.aggregateProfiles && !p.pidLabel, p.aggregationOptions
}
