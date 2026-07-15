package main

import (
	"fmt"
	"io"
	"math"
	"net"
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
	unavailable bool
	lastSuccess time.Time
}

func newRollingAverages() *rollingAverages {
	return &rollingAverages{}
}

func (a *rollingAverages) observe(reading measurement) {
	if math.IsNaN(reading.Value) || !reading.LatencyValid {
		a.markUnavailable()
		return
	}
	a.add(reading.Timestamp, reading.Value, reading.Latency)
}

func (a *rollingAverages) markUnavailable() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.unavailable = true
}

func (a *rollingAverages) add(timestamp time.Time, value float64, latency time.Duration) {
	a.mu.Lock()
	defer a.mu.Unlock()

	a.lastValue = value
	a.hasLast = true
	a.lastLatency = latency
	a.hasLatency = true
	a.unavailable = false
	a.lastSuccess = timestamp
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

func (a *rollingAverages) metricsTime(now time.Time) time.Time {
	a.mu.RLock()
	defer a.mu.RUnlock()
	if a.unavailable && a.hasLast {
		return a.lastSuccess
	}
	return now
}

func (a *rollingAverages) isUnavailable() bool {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.unavailable
}

type metricsHandler struct {
	averages   *rollingAverages
	name       string
	retainLast bool
	log        io.Writer
	now        func() time.Time
}

func (h metricsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/metrics" {
		http.NotFound(w, r)
		return
	}
	now := time.Now()
	if h.now != nil {
		now = h.now()
	}
	if h.log != nil {
		_ = writeTimestamped(h.log, now, "served metrics to client=%s user_agent=%q\n", clientIP(r.RemoteAddr), r.UserAgent())
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	renderMetrics(w, h.averages, h.name, now, h.retainLast)
}

func clientIP(remoteAddress string) string {
	host, _, err := net.SplitHostPort(remoteAddress)
	if err == nil {
		return host
	}
	return remoteAddress
}

func renderMetrics(w io.Writer, averages *rollingAverages, name string, now time.Time, retainLast bool) {
	metricsTime := averages.metricsTime(now)
	unavailable := averages.isUnavailable()
	reportUnavailable := unavailable && !retainLast
	fmt.Fprintf(w, "# HELP %s_last Most recent successful sensor level in centimeters, or NaN before the first successful reading.\n", name)
	fmt.Fprintf(w, "# TYPE %s_last gauge\n", name)
	if value, ok := averages.last(); ok && !reportUnavailable {
		fmt.Fprintf(w, "%s_last %g\n", name, value)
	} else {
		fmt.Fprintf(w, "%s_last NaN\n", name)
	}
	fmt.Fprintf(w, "# HELP %s_latency_seconds Time from sending the latest Modbus command until its complete response arrived, or NaN when the latest reading failed.\n", name)
	fmt.Fprintf(w, "# TYPE %s_latency_seconds gauge\n", name)
	if latency, ok := averages.latency(); ok && !unavailable {
		fmt.Fprintf(w, "%s_latency_seconds %g\n", name, latency.Seconds())
	} else {
		fmt.Fprintf(w, "%s_latency_seconds NaN\n", name)
	}
	fmt.Fprintf(w, "# HELP %s_average Average successful sensor level in centimeters within the rolling time window.\n", name)
	fmt.Fprintf(w, "# TYPE %s_average gauge\n", name)
	for _, window := range averageWindows {
		if reportUnavailable {
			fmt.Fprintf(w, "%s_average{window_seconds=\"%d\"} NaN\n", name, int(window.Seconds()))
			continue
		}
		value, ok := averages.average(metricsTime, window)
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
