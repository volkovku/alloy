package ebpf

import (
	"testing"

	"github.com/grafana/alloy/syntax"
	"github.com/stretchr/testify/require"
)

func TestAggregateProfilesArguments(t *testing.T) {
	for _, tc := range []struct {
		config    string
		aggregate bool
		invalid   bool
	}{
		{config: "forward_to = []"},
		{config: "forward_to = []\naggregate_profiles = true", aggregate: true},
		{config: "forward_to = []\naggregate_profiles = true\npid_label = false", aggregate: true},
		{config: "forward_to = []\naggregate_profiles = true\npid_label = true", invalid: true},
		{config: "forward_to = []\naggregate_profiles = false\npid_label = true"},
	} {
		t.Run(tc.config, func(t *testing.T) {
			var args Arguments
			err := syntax.Unmarshal([]byte(tc.config), &args)
			if tc.invalid {
				require.ErrorContains(t, err, "aggregate_profiles requires pid_label to be false")
				return
			}
			require.NoError(t, err)
			require.Equal(t, tc.aggregate, args.AggregateProfiles)
		})
	}
}

func TestAggregationLimitsArguments(t *testing.T) {
	for _, config := range []string{
		"aggregate_max_stack_depth = -1",
		"aggregate_min_sample_percent = -1",
		"aggregate_min_sample_percent = 101",
		"aggregate_min_sample_value = -1",
	} {
		var a Arguments
		require.Error(t, syntax.Unmarshal([]byte("forward_to = []\n"+config), &a))
	}
	var a Arguments
	require.NoError(t, syntax.Unmarshal([]byte(`forward_to = []
aggregate_profiles = true
aggregate_max_stack_depth = 64
aggregate_min_sample_percent = 0.1
aggregate_min_sample_value = 1000000
`), &a))
	require.Equal(t, 64, a.AggregationOptions.MaxStackDepth)
	require.Equal(t, 0.1, a.AggregationOptions.MinSamplePercent)
	require.EqualValues(t, 1000000, a.AggregationOptions.MinSampleValue)
}
