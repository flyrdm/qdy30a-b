package main

import (
	"bytes"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestRollingAverages(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	averages := newRollingAverages()
	averages.add(now.Add(-25*time.Second), 10, 10*time.Millisecond)
	averages.add(now.Add(-8*time.Second), 20, 20*time.Millisecond)
	averages.add(now.Add(-2*time.Second), 40, 30*time.Millisecond)

	tests := []struct {
		window time.Duration
		want   float64
	}{
		{5 * time.Second, 40},
		{10 * time.Second, 30},
		{20 * time.Second, 30},
		{30 * time.Second, 70.0 / 3.0},
	}
	for _, tt := range tests {
		got, ok := averages.average(now, tt.window)
		if !ok || got != tt.want {
			t.Errorf("average(%s) = %g, %v; want %g, true", tt.window, got, ok, tt.want)
		}
	}
	if _, ok := averages.average(now.Add(time.Hour), 300*time.Second); ok {
		t.Fatal("expired samples contributed to average")
	}
}

func TestMetricsHandler(t *testing.T) {
	now := time.Now()
	averages := newRollingAverages()
	averages.add(now, 12.5, 25*time.Millisecond)

	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	response := httptest.NewRecorder()
	metricsHandler{averages: averages, name: "rain_water_tank_level"}.ServeHTTP(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d", response.Code)
	}
	for _, want := range []string{
		"Most recent successful sensor level in centimeters",
		"# TYPE rain_water_tank_level_last gauge",
		"rain_water_tank_level_last 12.5",
		"# TYPE rain_water_tank_level_latency_seconds gauge",
		"rain_water_tank_level_latency_seconds 0.025",
		"# TYPE rain_water_tank_level_average gauge",
		`rain_water_tank_level_average{window_seconds="5"} 12.5`,
		`rain_water_tank_level_average{window_seconds="300"} 12.5`,
	} {
		if !strings.Contains(response.Body.String(), want) {
			t.Errorf("metrics do not contain %q:\n%s", want, response.Body.String())
		}
	}

	var empty bytes.Buffer
	renderMetrics(&empty, newRollingAverages(), "qdy30a_b_value", now, true)
	if !strings.Contains(empty.String(), "qdy30a_b_value_last NaN") {
		t.Fatalf("empty metrics = %q", empty.String())
	}
	if !strings.Contains(empty.String(), "qdy30a_b_value_latency_seconds NaN") {
		t.Fatalf("empty metrics = %q", empty.String())
	}
	if !strings.Contains(empty.String(), `qdy30a_b_value_average{window_seconds="5"} NaN`) {
		t.Fatalf("empty metrics = %q", empty.String())
	}
}

func TestMetricsHandlerLogsClient(t *testing.T) {
	now := time.Date(2026, 7, 15, 16, 36, 12, 123456789, time.UTC)
	request := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	request.RemoteAddr = "[2001:db8::1]:4321"
	request.Header.Set("User-Agent", "Prometheus/3.0")
	response := httptest.NewRecorder()
	var log bytes.Buffer
	metricsHandler{
		averages:   newRollingAverages(),
		name:       "sensor",
		retainLast: true,
		log:        &log,
		now:        func() time.Time { return now },
	}.ServeHTTP(response, request)

	if got, want := log.String(), formatTimestamp(now)+" served metrics to client=2001:db8::1 user_agent=\"Prometheus/3.0\"\n"; got != want {
		t.Fatalf("metrics log = %q, want %q", got, want)
	}
}

func TestNormalizeMetricName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: "rain-water-tank-level", want: "rain_water_tank_level"},
		{input: "tank level", want: "tank_level"},
		{input: "2nd_tank", want: "_2nd_tank"},
		{input: "level", want: "level"},
	}
	for _, tt := range tests {
		got, err := normalizeMetricName(tt.input)
		if err != nil || got != tt.want {
			t.Errorf("normalizeMetricName(%q) = %q, %v; want %q, nil", tt.input, got, err, tt.want)
		}
	}
	for _, input := range []string{"", "---"} {
		if _, err := normalizeMetricName(input); err == nil {
			t.Errorf("normalizeMetricName(%q) returned nil error", input)
		}
	}
}

func TestUnavailableReadingKeepsLastGoodMetrics(t *testing.T) {
	now := time.Now()
	averages := newRollingAverages()
	averages.observe(measurement{
		Timestamp:    now,
		Value:        12.5,
		Latency:      25 * time.Millisecond,
		LatencyValid: true,
	})
	averages.observe(measurement{
		Timestamp: now.Add(time.Second),
		Value:     math.NaN(),
	})

	last, ok := averages.last()
	if !ok || last != 12.5 {
		t.Fatalf("last = %g, %v; want 12.5, true", last, ok)
	}
	latency, ok := averages.latency()
	if !ok || latency != 25*time.Millisecond {
		t.Fatalf("latency = %s, %v; want 25ms, true", latency, ok)
	}
	average, ok := averages.average(now, 5*time.Second)
	if !ok || average != 12.5 {
		t.Fatalf("average = %g, %v; want 12.5, true", average, ok)
	}

	var rendered bytes.Buffer
	renderMetrics(&rendered, averages, "sensor", now.Add(10*time.Minute), true)
	for _, want := range []string{
		"sensor_last 12.5",
		"sensor_latency_seconds NaN",
		`sensor_average{window_seconds="5"} 12.5`,
		`sensor_average{window_seconds="300"} 12.5`,
	} {
		if !strings.Contains(rendered.String(), want) {
			t.Errorf("stale metrics do not contain %q:\n%s", want, rendered.String())
		}
	}

	var unavailable bytes.Buffer
	renderMetrics(&unavailable, averages, "sensor", now.Add(10*time.Minute), false)
	for _, want := range []string{
		"sensor_last NaN",
		"sensor_latency_seconds NaN",
		`sensor_average{window_seconds="5"} NaN`,
		`sensor_average{window_seconds="300"} NaN`,
	} {
		if !strings.Contains(unavailable.String(), want) {
			t.Errorf("unavailable metrics do not contain %q:\n%s", want, unavailable.String())
		}
	}
}
