package stats

import (
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIntSeries(t *testing.T) {
	type results struct {
		len                  uint
		min, max, total, avg int
		str                  string
	}
	tests := []struct {
		name    string
		samples []int
		format  string
		want    results
	}{
		{
			name:    "No samples",
			samples: []int{},
			format:  "%v",
			want:    results{str: "min: 0 max: 0 avg: 0"},
		},
		{
			name:    "3 samples",
			samples: []int{2, 8, 8},
			format:  "Stats: %3d",
			want: results{
				len:   3,
				min:   2,
				max:   8,
				total: 18,
				avg:   6,
				str:   "Stats: min:   2 max:   8 avg:   6",
			},
		},
		{
			name:    "decreasing values update min",
			samples: []int{8, 2, 5},
			format:  "%d",
			want: results{
				len:   3,
				min:   2,
				max:   8,
				total: 15,
				avg:   5,
				str:   "min: 2 max: 8 avg: 5",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var series Series[int]
			for _, sample := range tt.samples {
				series.Add(sample)
			}
			assert.Equal(t, tt.want.len, series.Len())
			assert.Equal(t, tt.want.min, series.Min())
			assert.Equal(t, tt.want.max, series.Max())
			assert.Equal(t, tt.want.total, series.Total())
			assert.Equal(t, tt.want.avg, series.Avg())
			assert.Equal(t, tt.want.str, fmt.Sprintf(tt.format, series))
		})
	}
}

func TestSeriesFormatPrecision(t *testing.T) {
	var s Series[float64]
	for _, v := range []float64{8.0, 2.0, 5.0} {
		s.Add(v)
	}
	assert.Equal(t, "min: 2.00 max: 8.00 avg: 5.00", fmt.Sprintf("%.2f", s))
	assert.Equal(t, "min: 2 max: 8 avg: 5", fmt.Sprintf("%.0f", s))
}
