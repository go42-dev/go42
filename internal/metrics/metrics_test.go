package metrics

import (
	"testing"
)

func TestConstructMetric(t *testing.T) {
	tests := []struct {
		name         string
		metricName   string
		labels       map[string]any
		globalLabels map[string]any
		want         string
	}{
		{
			name:       "empty labels",
			metricName: "test_metric",
			labels:     nil,
			want:       "test_metric",
		},
		{
			name:       "single label",
			metricName: "http_requests_total",
			labels: map[string]any{
				"method": "GET",
			},
			want: `http_requests_total{method="GET"}`,
		},
		{
			name:       "multiple labels",
			metricName: "memory_usage",
			labels: map[string]any{
				"host":     "server1",
				"instance": "prod",
				"region":   "us-east",
			},
			want: `memory_usage{host="server1",instance="prod",region="us-east"}`,
		},
		{
			name:       "labels with different value types",
			metricName: "mixed_metric",
			labels: map[string]any{
				"bool":   true,
				"int":    42,
				"string": "value",
			},
			want: `mixed_metric{bool="true",int="42",string="value"}`,
		},
		{
			name:       "escaped label value",
			metricName: "escaped_metric",
			labels: map[string]any{
				"value": "path\\to\"thing\"\nnext",
			},
			want: `escaped_metric{value="path\\to\"thing\"\nnext"}`,
		},
		{
			name:       "with global labels",
			metricName: "test_metric",
			labels: map[string]any{
				"local": "value",
			},
			globalLabels: map[string]any{
				"env":     "prod",
				"service": "api",
			},
			want: `test_metric{env="prod",local="value",service="api"}`,
		},
		{
			name:       "global labels override",
			metricName: "override_metric",
			labels: map[string]any{
				"env": "dev",
			},
			globalLabels: map[string]any{
				"env": "prod",
			},
			want: `override_metric{env="dev"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Reset and set global labels for this test
			globalLabels = make(map[string]any)
			if tt.globalLabels != nil {
				RegisterGlobalLabels(tt.globalLabels)
			}

			got := constructMetric(tt.metricName, tt.labels)
			if got != tt.want {
				t.Errorf("constructMetric() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEscapeLabelValue(t *testing.T) {
	tests := []struct {
		name  string
		value any
		want  string
	}{
		{name: "plain", value: "value", want: "value"},
		{name: "backslash", value: `path\file`, want: `path\\file`},
		{name: "quote", value: `say "hello"`, want: `say \"hello\"`},
		{name: "newline", value: "first\nsecond", want: `first\nsecond`},
		{name: "non-string", value: 42, want: "42"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := escapeLabelValue(tt.value); got != tt.want {
				t.Errorf("escapeLabelValue() = %q, want %q", got, tt.want)
			}
		})
	}
}
