package main

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"
)

var averageWindows = []time.Duration{
	5 * time.Second,
	10 * time.Second,
	20 * time.Second,
	30 * time.Second,
	60 * time.Second,
	300 * time.Second,
}

type valueSample struct {
	timestamp time.Time
	value     float64
}

type rollingAverages struct {
	mu          sync.RWMutex
	samples     []valueSample
	lastValue   float64
	hasLast     bool
	lastLatency time.Duration
	hasLatency  bool
}

func newRollingAverages() *rollingAverages {
	return &rollingAverages{}
}

func (a *rollingAverages) add(timestamp time.Time, value float64, latency time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.lastValue = value
	a.hasLast = true
	a.lastLatency = latency
	a.hasLatency = true
	a.samples = append(a.samples, valueSample{timestamp: timestamp, value: value})
	cutoff := timestamp.Add(-averageWindows[len(averageWindows)-1])
	first := 0
	for first < len(a.samples) && a.samples[first].timestamp.Before(cutoff) {
		first++
	}
	if first > 0 {
		a.samples = append([]valueSample(nil), a.samples[first:]...)
	}
}

func (a *rollingAverages) latency() (time.Duration, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.lastLatency, a.hasLatency
}

func (a *rollingAverages) setLast(value float64) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.lastValue = value
	a.hasLast = true
}

func (a *rollingAverages) last() (float64, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.lastValue, a.hasLast
}

func (a *rollingAverages) average(now time.Time, window time.Duration) (float64, bool) {
	a.mu.RLock()
	defer a.mu.RUnlock()

	cutoff := now.Add(-window)
	var sum float64
	var count int
	for i := len(a.samples) - 1; i >= 0; i-- {
		sample := a.samples[i]
		if sample.timestamp.Before(cutoff) {
			break
		}
		if sample.timestamp.After(now) {
			continue
		}
		sum += sample.value
		count++
	}
	if count == 0 {
		return 0, false
	}
	return sum / float64(count), true
}

type metricsHandler struct {
	averages *rollingAverages
	name     string
}

func (h metricsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/metrics" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	renderMetrics(w, h.averages, h.name, time.Now())
}

func renderMetrics(w io.Writer, averages *rollingAverages, name string, now time.Time) {
	fmt.Fprintf(w, "# HELP %s_last Most recent emitted sensor level in centimeters, or NaN when no reading is available.\n", name)
	fmt.Fprintf(w, "# TYPE %s_last gauge\n", name)
	if value, ok := averages.last(); ok {
		fmt.Fprintf(w, "%s_last %g\n", name, value)
	} else {
		fmt.Fprintf(w, "%s_last NaN\n", name)
	}
	fmt.Fprintf(w, "# HELP %s_latency_seconds Time from sending the latest successful Modbus command until its complete response arrived.\n", name)
	fmt.Fprintf(w, "# TYPE %s_latency_seconds gauge\n", name)
	if latency, ok := averages.latency(); ok {
		fmt.Fprintf(w, "%s_latency_seconds %g\n", name, latency.Seconds())
	} else {
		fmt.Fprintf(w, "%s_latency_seconds NaN\n", name)
	}
	fmt.Fprintf(w, "# HELP %s_average Average successful sensor level in centimeters within the rolling time window.\n", name)
	fmt.Fprintf(w, "# TYPE %s_average gauge\n", name)
	for _, window := range averageWindows {
		value, ok := averages.average(now, window)
		if !ok {
			fmt.Fprintf(w, "%s_average{window_seconds=\"%d\"} NaN\n", name, int(window.Seconds()))
			continue
		}
		fmt.Fprintf(w, "%s_average{window_seconds=\"%d\"} %g\n", name, int(window.Seconds()), value)
	}
}

func normalizeMetricName(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("-metrics-name cannot be empty")
	}
	var normalized strings.Builder
	hasLetterOrDigit := false
	for _, char := range name {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' {
			normalized.WriteRune(char)
			hasLetterOrDigit = true
		} else {
			normalized.WriteByte('_')
		}
	}
	if !hasLetterOrDigit {
		return "", fmt.Errorf("-metrics-name %q must contain a letter or digit", name)
	}
	result := normalized.String()
	if result[0] >= '0' && result[0] <= '9' {
		result = "_" + result
	}
	return result, nil
}
