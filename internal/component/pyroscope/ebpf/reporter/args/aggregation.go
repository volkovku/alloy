package args

import (
	"fmt"
	"math"
)

// AggregationOptions controls postprocessing of each aggregated profile.
// Zero values disable the corresponding limits.
type AggregationOptions struct {
	MaxStackDepth    int     `alloy:"aggregate_max_stack_depth,attr,optional"`
	MinSamplePercent float64 `alloy:"aggregate_min_sample_percent,attr,optional"`
	MinSampleValue   int64   `alloy:"aggregate_min_sample_value,attr,optional"`
}

func (o AggregationOptions) Validate() error {
	if o.MaxStackDepth < 0 {
		return fmt.Errorf("aggregate_max_stack_depth must be non-negative")
	}
	if math.IsNaN(o.MinSamplePercent) || math.IsInf(o.MinSamplePercent, 0) || o.MinSamplePercent < 0 || o.MinSamplePercent > 100 {
		return fmt.Errorf("aggregate_min_sample_percent must be between 0 and 100")
	}
	if o.MinSampleValue < 0 {
		return fmt.Errorf("aggregate_min_sample_value must be non-negative")
	}
	return nil
}
