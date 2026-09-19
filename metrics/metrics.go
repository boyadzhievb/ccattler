// Package metrics provides lightweight Prometheus-compatible metrics
// collection without external dependencies. It supports counters, gauges,
// and histograms, and serves them in Prometheus text exposition format.
package metrics

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Registry holds all registered metrics and serves them via HTTP.
type Registry struct {
	mutex           sync.RWMutex
	counters        map[string]*Counter
	gauges          map[string]*Gauge
	histograms      map[string]*Histogram
	registrationOrder []string
}

// NewRegistry creates an empty metrics registry.
func NewRegistry() *Registry {
	return &Registry{
		counters:   make(map[string]*Counter),
		gauges:     make(map[string]*Gauge),
		histograms: make(map[string]*Histogram),
	}
}

// DefaultRegistry is the process-wide metrics registry.
var DefaultRegistry = NewRegistry()

// Counter is a monotonically increasing metric.
type Counter struct {
	name        string
	help        string
	labelNames  []string
	mutex       sync.RWMutex
	values      map[string]*atomic.Int64
}

// Gauge is a metric that can go up and down.
type Gauge struct {
	name       string
	help       string
	labelNames []string
	mutex      sync.RWMutex
	values     map[string]*atomic.Int64
}

// Histogram tracks the distribution of observed values across predefined buckets.
type Histogram struct {
	name       string
	help       string
	labelNames []string
	buckets    []float64
	mutex      sync.RWMutex
	series     map[string]*histogramSeries
}

type histogramSeries struct {
	bucketCounts []atomic.Int64
	totalSum     atomic.Int64
	totalCount   atomic.Int64
}

// RegisterCounter creates and registers a new counter metric.
func (registry *Registry) RegisterCounter(name string, help string, labelNames ...string) *Counter {
	counter := &Counter{
		name:       name,
		help:       help,
		labelNames: labelNames,
		values:     make(map[string]*atomic.Int64),
	}
	registry.mutex.Lock()
	registry.counters[name] = counter
	registry.registrationOrder = append(registry.registrationOrder, "counter:"+name)
	registry.mutex.Unlock()
	return counter
}

// RegisterGauge creates and registers a new gauge metric.
func (registry *Registry) RegisterGauge(name string, help string, labelNames ...string) *Gauge {
	gauge := &Gauge{
		name:       name,
		help:       help,
		labelNames: labelNames,
		values:     make(map[string]*atomic.Int64),
	}
	registry.mutex.Lock()
	registry.gauges[name] = gauge
	registry.registrationOrder = append(registry.registrationOrder, "gauge:"+name)
	registry.mutex.Unlock()
	return gauge
}

// RegisterHistogram creates and registers a new histogram metric with the
// given bucket boundaries.
func (registry *Registry) RegisterHistogram(name string, help string, buckets []float64, labelNames ...string) *Histogram {
	sortedBuckets := make([]float64, len(buckets))
	copy(sortedBuckets, buckets)
	sort.Float64s(sortedBuckets)

	histogram := &Histogram{
		name:       name,
		help:       help,
		labelNames: labelNames,
		buckets:    sortedBuckets,
		series:     make(map[string]*histogramSeries),
	}
	registry.mutex.Lock()
	registry.histograms[name] = histogram
	registry.registrationOrder = append(registry.registrationOrder, "histogram:"+name)
	registry.mutex.Unlock()
	return histogram
}

// Inc increments the counter by 1 for the given label values.
func (counter *Counter) Inc(labelValues ...string) {
	counter.Add(1, labelValues...)
}

// Add increments the counter by the given delta for the given label values.
func (counter *Counter) Add(delta int64, labelValues ...string) {
	labelKey := joinLabelValues(labelValues)
	counter.mutex.RLock()
	atomicValue, exists := counter.values[labelKey]
	counter.mutex.RUnlock()
	if exists {
		atomicValue.Add(delta)
		return
	}
	counter.mutex.Lock()
	atomicValue, exists = counter.values[labelKey]
	if !exists {
		atomicValue = &atomic.Int64{}
		counter.values[labelKey] = atomicValue
	}
	counter.mutex.Unlock()
	atomicValue.Add(delta)
}

// Set sets the gauge to the given value for the given label values.
func (gauge *Gauge) Set(value int64, labelValues ...string) {
	labelKey := joinLabelValues(labelValues)
	gauge.mutex.RLock()
	atomicValue, exists := gauge.values[labelKey]
	gauge.mutex.RUnlock()
	if exists {
		atomicValue.Store(value)
		return
	}
	gauge.mutex.Lock()
	atomicValue, exists = gauge.values[labelKey]
	if !exists {
		atomicValue = &atomic.Int64{}
		gauge.values[labelKey] = atomicValue
	}
	gauge.mutex.Unlock()
	atomicValue.Store(value)
}

// Inc increments the gauge by 1.
func (gauge *Gauge) Inc(labelValues ...string) {
	labelKey := joinLabelValues(labelValues)
	gauge.mutex.RLock()
	atomicValue, exists := gauge.values[labelKey]
	gauge.mutex.RUnlock()
	if exists {
		atomicValue.Add(1)
		return
	}
	gauge.mutex.Lock()
	atomicValue, exists = gauge.values[labelKey]
	if !exists {
		atomicValue = &atomic.Int64{}
		gauge.values[labelKey] = atomicValue
	}
	gauge.mutex.Unlock()
	atomicValue.Add(1)
}

// Dec decrements the gauge by 1.
func (gauge *Gauge) Dec(labelValues ...string) {
	labelKey := joinLabelValues(labelValues)
	gauge.mutex.RLock()
	atomicValue, exists := gauge.values[labelKey]
	gauge.mutex.RUnlock()
	if exists {
		atomicValue.Add(-1)
		return
	}
	gauge.mutex.Lock()
	atomicValue, exists = gauge.values[labelKey]
	if !exists {
		atomicValue = &atomic.Int64{}
		gauge.values[labelKey] = atomicValue
	}
	gauge.mutex.Unlock()
	atomicValue.Add(-1)
}

// Observe records a value in the histogram for the given label values.
// The value is stored as microseconds (multiply seconds by 1e6 before calling).
func (histogram *Histogram) Observe(valueMicros int64, labelValues ...string) {
	labelKey := joinLabelValues(labelValues)
	histogram.mutex.RLock()
	series, exists := histogram.series[labelKey]
	histogram.mutex.RUnlock()
	if !exists {
		histogram.mutex.Lock()
		series, exists = histogram.series[labelKey]
		if !exists {
			series = &histogramSeries{
				bucketCounts: make([]atomic.Int64, len(histogram.buckets)),
			}
			histogram.series[labelKey] = series
		}
		histogram.mutex.Unlock()
	}

	series.totalSum.Add(valueMicros)
	series.totalCount.Add(1)
	valueSeconds := float64(valueMicros) / 1e6
	for bucketIndex, boundary := range histogram.buckets {
		if valueSeconds <= boundary {
			series.bucketCounts[bucketIndex].Add(1)
			break
		}
	}
}

// ObserveDuration records a duration observation, converting to the histogram's
// internal microsecond representation.
func (histogram *Histogram) ObserveDuration(duration time.Duration, labelValues ...string) {
	histogram.Observe(duration.Microseconds(), labelValues...)
}

// Timer returns the current time for later use with ObserveSince.
func Timer() time.Time {
	return time.Now()
}

// ObserveSince records the time elapsed since the given start time.
func (histogram *Histogram) ObserveSince(startTime time.Time, labelValues ...string) {
	histogram.ObserveDuration(time.Since(startTime), labelValues...)
}

// Handler returns an HTTP handler that serves all registered metrics in
// Prometheus text exposition format.
func (registry *Registry) Handler() http.Handler {
	return http.HandlerFunc(func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		registry.mutex.RLock()
		defer registry.mutex.RUnlock()

		var builder strings.Builder

		for _, registrationKey := range registry.registrationOrder {
			parts := strings.SplitN(registrationKey, ":", 2)
			metricType, metricName := parts[0], parts[1]

			switch metricType {
			case "counter":
				counter := registry.counters[metricName]
				writeCounterText(&builder, counter)
			case "gauge":
				gauge := registry.gauges[metricName]
				writeGaugeText(&builder, gauge)
			case "histogram":
				histogram := registry.histograms[metricName]
				writeHistogramText(&builder, histogram)
			}
		}

		responseWriter.Write([]byte(builder.String()))
	})
}

func writeCounterText(builder *strings.Builder, counter *Counter) {
	fmt.Fprintf(builder, "# HELP %s %s\n", counter.name, counter.help)
	fmt.Fprintf(builder, "# TYPE %s counter\n", counter.name)
	counter.mutex.RLock()
	defer counter.mutex.RUnlock()

	sortedKeys := sortedMapKeys(counter.values)
	for _, labelKey := range sortedKeys {
		atomicValue := counter.values[labelKey]
		labelString := formatLabelString(counter.labelNames, labelKey)
		fmt.Fprintf(builder, "%s%s %d\n", counter.name, labelString, atomicValue.Load())
	}
}

func writeGaugeText(builder *strings.Builder, gauge *Gauge) {
	fmt.Fprintf(builder, "# HELP %s %s\n", gauge.name, gauge.help)
	fmt.Fprintf(builder, "# TYPE %s gauge\n", gauge.name)
	gauge.mutex.RLock()
	defer gauge.mutex.RUnlock()

	sortedKeys := sortedMapKeys(gauge.values)
	for _, labelKey := range sortedKeys {
		atomicValue := gauge.values[labelKey]
		labelString := formatLabelString(gauge.labelNames, labelKey)
		fmt.Fprintf(builder, "%s%s %d\n", gauge.name, labelString, atomicValue.Load())
	}
}

func writeHistogramText(builder *strings.Builder, histogram *Histogram) {
	fmt.Fprintf(builder, "# HELP %s %s\n", histogram.name, histogram.help)
	fmt.Fprintf(builder, "# TYPE %s histogram\n", histogram.name)
	histogram.mutex.RLock()
	defer histogram.mutex.RUnlock()

	sortedKeys := sortedSeriesKeys(histogram.series)
	for _, labelKey := range sortedKeys {
		series := histogram.series[labelKey]
		baseLabelString := formatLabelString(histogram.labelNames, labelKey)

		var cumulativeCount int64
		for bucketIndex, boundary := range histogram.buckets {
			cumulativeCount += series.bucketCounts[bucketIndex].Load()
			bucketLabel := addLabel(baseLabelString, "le", formatFloat(boundary))
			fmt.Fprintf(builder, "%s_bucket%s %d\n", histogram.name, bucketLabel, cumulativeCount)
		}
		infLabel := addLabel(baseLabelString, "le", "+Inf")
		fmt.Fprintf(builder, "%s_bucket%s %d\n", histogram.name, infLabel, series.totalCount.Load())

		sumSeconds := float64(series.totalSum.Load()) / 1e6
		fmt.Fprintf(builder, "%s_sum%s %s\n", histogram.name, baseLabelString, formatFloat(sumSeconds))
		fmt.Fprintf(builder, "%s_count%s %d\n", histogram.name, baseLabelString, series.totalCount.Load())
	}
}

func joinLabelValues(labelValues []string) string {
	return strings.Join(labelValues, "\x00")
}

func formatLabelString(labelNames []string, labelKey string) string {
	if len(labelNames) == 0 {
		return ""
	}
	labelValues := strings.Split(labelKey, "\x00")
	var pairs []string
	for labelIndex, labelName := range labelNames {
		labelValue := ""
		if labelIndex < len(labelValues) {
			labelValue = labelValues[labelIndex]
		}
		pairs = append(pairs, fmt.Sprintf("%s=%q", labelName, labelValue))
	}
	return "{" + strings.Join(pairs, ",") + "}"
}

func addLabel(existingLabels string, labelName string, labelValue string) string {
	newLabel := fmt.Sprintf("%s=%q", labelName, labelValue)
	if existingLabels == "" {
		return "{" + newLabel + "}"
	}
	return existingLabels[:len(existingLabels)-1] + "," + newLabel + "}"
}

func formatFloat(value float64) string {
	if value == math.Inf(1) {
		return "+Inf"
	}
	if value == float64(int64(value)) {
		return fmt.Sprintf("%.1f", value)
	}
	return fmt.Sprintf("%g", value)
}

func sortedMapKeys(valueMap map[string]*atomic.Int64) []string {
	keys := make([]string, 0, len(valueMap))
	for labelKey := range valueMap {
		keys = append(keys, labelKey)
	}
	sort.Strings(keys)
	return keys
}

func sortedSeriesKeys(seriesMap map[string]*histogramSeries) []string {
	keys := make([]string, 0, len(seriesMap))
	for labelKey := range seriesMap {
		keys = append(keys, labelKey)
	}
	sort.Strings(keys)
	return keys
}

// DurationBuckets returns standard duration bucket boundaries in seconds,
// suitable for reconciliation and request latency histograms.
func DurationBuckets() []float64 {
	return []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1.0, 2.5, 5.0, 10.0}
}
