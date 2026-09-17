package metrics

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	vmetrics "github.com/VictoriaMetrics/metrics"
)

var (
	mutex        sync.RWMutex
	globalLabels map[string]any
	labelEscaper = strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		"\n", `\n`,
	)
)

func init() {
	globalLabels = make(map[string]any)
}

func RegisterGlobalLabels(labels map[string]any) {
	if len(labels) == 0 {
		return
	}
	mutex.Lock()
	defer mutex.Unlock()
	for k, v := range labels {
		globalLabels[k] = v
	}
}

func Counter(name string, labels map[string]any) *vmetrics.Counter {
	return vmetrics.GetOrCreateCounter(constructMetric(name, labels))
}

func Gauge(name string, labels map[string]any) *vmetrics.Gauge {
	return vmetrics.GetOrCreateGauge(constructMetric(name, labels), nil)
}

func Histogram(name string, labels map[string]any) *vmetrics.Histogram {
	return vmetrics.GetOrCreateHistogram(constructMetric(name, labels))
}

// constructMetric takes metrics name and labels, and returns a string representation of the metric.
// It also adds global labels to the metric if they are set.
// It is a little bit more complicated than just appending labels to the name, because
// we need to sort the labels to make sure that the same labels in different order
// will not create different metrics.
func constructMetric(name string, labels map[string]any) string {
	// mutex actually may not be needed at all if we don't modify globalLabels
	// aside from single call in main.go, but keep it for safety anyway
	mutex.RLock()
	defer mutex.RUnlock()

	if len(labels) == 0 && len(globalLabels) == 0 {
		return name
	}

	totalLabels := len(labels)
	globalLabelsCount := len(globalLabels)

	if globalLabelsCount > 0 {
		for k := range globalLabels {
			if _, exists := labels[k]; !exists {
				totalLabels++
			}
		}
	}

	if totalLabels == 0 {
		return name
	}

	keys := make([]string, 0, totalLabels)

	for k := range labels {
		keys = append(keys, k)
	}
	if globalLabelsCount > 0 {
		for k := range globalLabels {
			if _, exists := labels[k]; !exists {
				keys = append(keys, k)
			}
		}
	}

	sort.Strings(keys)

	var builder strings.Builder
	builder.WriteString(name)
	builder.WriteByte('{')

	for i, k := range keys {
		if i > 0 {
			builder.WriteByte(',')
		}

		builder.WriteString(k)
		builder.WriteString(`="`)

		var v any
		if val, exists := labels[k]; exists {
			v = val
		} else {
			v = globalLabels[k]
		}

		builder.WriteString(escapeLabelValue(v))
		builder.WriteByte('"')
	}

	builder.WriteByte('}')

	return builder.String()
}

func escapeLabelValue(value any) string {
	// fmt.Sprintf may be slow, but it is a trade-off for accepting arbitrary label values.
	return labelEscaper.Replace(fmt.Sprintf("%v", value)) //nolint:staticcheck
}
