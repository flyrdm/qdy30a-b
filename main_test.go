package main

import (
	"bytes"
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"go.bug.st/serial"
)

type codedPortError struct{ code serial.PortErrorCode }

func (e codedPortError) Error() string              { return "serial error" }
func (e codedPortError) Code() serial.PortErrorCode { return e.code }

func TestParseOptions(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "baud without address", args: []string{"-baud", "9600"}, want: "provided together"},
		{name: "address without baud", args: []string{"-address", "1"}, want: "provided together"},
		{name: "bad address", args: []string{"-baud", "9600", "-address", "248"}, want: "between 1 and 247"},
		{name: "negative interval", args: []string{"-interval", "-1s"}, want: "cannot be negative"},
		{name: "discovery conflict", args: []string{"-discover", "-baud", "9600", "-address", "1"}, want: "cannot be combined"},
		{name: "parity without known parameters", args: []string{"-parity", "E"}, want: "only applies"},
		{name: "output conflict", args: []string{"-json", "-text"}, want: "cannot be combined"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseOptions(tt.args, &bytes.Buffer{})
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestMetricsRetainLastOption(t *testing.T) {
	opts, err := parseOptions(nil, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if !opts.metricsRetainLast {
		t.Fatal("-metrics-retain-last should default to true")
	}

	opts, err = parseOptions([]string{"-metrics-retain-last=false"}, &bytes.Buffer{})
	if err != nil {
		t.Fatal(err)
	}
	if opts.metricsRetainLast {
		t.Fatal("-metrics-retain-last=false was not applied")
	}
}

func TestPrintMeasurement(t *testing.T) {
	reading := measurement{
		Timestamp: time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC),
		Value:     12.3, Unit: "cm", UnitCode: 1, Raw: 123, Decimals: 1,
		Baud: 9600, Address: 1, Zero: 0, FullScale: 500,
		Latency: 25 * time.Millisecond, LatencyValid: true,
	}
	var human bytes.Buffer
	if err := printMeasurement(&human, reading, false, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(human.String(), "value=12.3 cm latency_seconds=0.025 raw=123") {
		t.Fatalf("human output = %q", human.String())
	}

	var encoded bytes.Buffer
	if err := printMeasurement(&encoded, reading, true, false); err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Timestamp      string   `json:"timestamp"`
		Value          *float64 `json:"value"`
		Raw            int16    `json:"raw"`
		Unit           string   `json:"unit"`
		Baud           int      `json:"baud"`
		LatencySeconds *float64 `json:"latency_seconds"`
	}
	if err := json.Unmarshal(encoded.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Timestamp != formatTimestamp(reading.Timestamp) || decoded.Value == nil || *decoded.Value != 12.3 || decoded.Raw != reading.Raw || decoded.Unit != reading.Unit || decoded.Baud != reading.Baud || decoded.LatencySeconds == nil || *decoded.LatencySeconds != 0.025 {
		t.Fatalf("JSON reading = %#v", decoded)
	}

	var text bytes.Buffer
	if err := printMeasurement(&text, reading, false, true); err != nil {
		t.Fatal(err)
	}
	if got, want := text.String(), formatTimestamp(reading.Timestamp)+"\tlevel_cm=12.3\tread_latency_seconds=0.025\n"; got != want {
		t.Fatalf("text output = %q, want %q", got, want)
	}
}

func TestUnavailableMeasurementOutputsMinusOne(t *testing.T) {
	previous := measurement{
		Value: 12.3, Unit: "cm", UnitCode: 1, Raw: 123, Decimals: 1,
		Baud: 9600, Address: 1, Zero: 0, FullScale: 500,
	}
	reading := unavailableMeasurement(configuration{baud: 4800, address: 2}, previous)
	if !math.IsNaN(reading.Value) || reading.Raw != -1 {
		t.Fatalf("unavailable reading = %#v, want value NaN and raw -1", reading)
	}
	if reading.Baud != 4800 || reading.Address != 2 || reading.Unit != "cm" {
		t.Fatalf("unavailable reading metadata = %#v", reading)
	}

	var text bytes.Buffer
	if err := printMeasurement(&text, reading, false, true); err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(text.String(), "\tlevel_cm=NaN\tread_latency_seconds=NaN\n") {
		t.Fatalf("text output = %q, want NaN value and latency", text.String())
	}
	var human bytes.Buffer
	if err := printMeasurement(&human, reading, false, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(human.String(), "value=NaN cm latency_seconds=NaN raw=-1") {
		t.Fatalf("human output = %q, want labeled NaN value and latency", human.String())
	}
	encoded, err := json.Marshal(reading)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Value          *float64 `json:"value"`
		LatencySeconds *float64 `json:"latency_seconds"`
	}
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Value != nil || decoded.LatencySeconds != nil {
		t.Fatalf("timeout JSON = %s, want null value and latency_seconds", encoded)
	}
}

func TestRunOutputsMinusOneAndReconnectsAfterTimeout(t *testing.T) {
	initial := &fakePort{}
	initial.onWrite = func(port *fakePort, _ []byte) {
		if len(port.writes) == 1 {
			port.response = responseFrame(1, 1, 3, 1, 1, 123, 0, 500)
		}
	}
	reconnected := &fakePort{}
	reconnected.onWrite = func(port *fakePort, _ []byte) {
		port.response = responseFrame(1, 1, 3, 1, 1, 124, 0, 500)
	}
	ports := []configurableSerialPort{initial, reconnected}
	originalOpen := openSerialPort
	openSerialPort = func(string, int, serial.Parity) (configurableSerialPort, error) {
		port := ports[0]
		ports = ports[1:]
		return port, nil
	}
	defer func() { openSerialPort = originalOpen }()

	var stdout, stderr bytes.Buffer
	if err := run([]string{"-baud", "9600", "-address", "1", "-text", "-timeout", "1ms"}, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) != 2 || !strings.HasSuffix(lines[0], "\tlevel_cm=NaN\tread_latency_seconds=NaN") {
		t.Fatalf("output lines = %#v, want timeout sentinel then recovered reading", lines)
	}
	recoveredFields := strings.Fields(lines[1])
	if len(recoveredFields) != 4 || recoveredFields[2] != "level_cm=12.4" || recoveredFields[3] == "read_latency_seconds=NaN" {
		t.Fatalf("recovered output = %q, want value and response latency", lines[1])
	}
	if !strings.Contains(stderr.String(), "output NaN and reconnecting") || !strings.Contains(stderr.String(), "reconnected") {
		t.Fatalf("stderr = %q", stderr.String())
	}
	if initial.closeCount == 0 || reconnected.closeCount == 0 {
		t.Fatalf("close counts = initial %d, reconnected %d", initial.closeCount, reconnected.closeCount)
	}
}

func TestFormatTimestampUsesLocalTimeAndMicrosecondPrecision(t *testing.T) {
	originalLocal := time.Local
	time.Local = time.FixedZone("CEST", 2*60*60)
	defer func() { time.Local = originalLocal }()

	timestamp := time.Date(2026, 7, 10, 11, 41, 34, 367146309, time.UTC)
	if got, want := formatTimestamp(timestamp), "2026-07-10 13:41:34.367146"; got != want {
		t.Fatalf("timestamp = %q, want %q", got, want)
	}

	encoded, err := json.Marshal(measurement{Timestamp: timestamp})
	if err != nil {
		t.Fatal(err)
	}
	if want := `"timestamp":"2026-07-10 13:41:34.367146"`; !bytes.Contains(encoded, []byte(want)) {
		t.Fatalf("JSON output = %s, want %s", encoded, want)
	}

	wholeSecond := time.Date(2026, 7, 10, 11, 41, 34, 0, time.UTC)
	if got, want := formatTimestamp(wholeSecond), "2026-07-10 13:41:34.000000"; got != want {
		t.Fatalf("whole-second timestamp = %q, want %q", got, want)
	}
}

func TestWriteTimestamped(t *testing.T) {
	timestamp := time.Date(2026, 7, 15, 16, 36, 12, 123456789, time.UTC)
	var output bytes.Buffer
	if err := writeTimestamped(&output, timestamp, "read sensor: %s\n", "timed out"); err != nil {
		t.Fatal(err)
	}
	if got, want := output.String(), formatTimestamp(timestamp)+" read sensor: timed out\n"; got != want {
		t.Fatalf("timestamped output = %q, want %q", got, want)
	}
}

func TestSerialOpenErrorGuidance(t *testing.T) {
	tests := []struct {
		code serial.PortErrorCode
		want string
	}{
		{serial.PermissionDenied, "setfacl"},
		{serial.PortNotFound, "does not exist"},
		{serial.PortBusy, "serial port is busy"},
	}
	for _, tt := range tests {
		err := serialOpenError("/dev/ttyUSB0", codedPortError{code: tt.code})
		if !strings.Contains(err.Error(), tt.want) {
			t.Fatalf("error = %q, want substring %q", err, tt.want)
		}
	}
}
