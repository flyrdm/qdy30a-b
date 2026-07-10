package main

import (
	"bytes"
	"encoding/json"
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

func TestPrintMeasurement(t *testing.T) {
	reading := measurement{
		Timestamp: time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC),
		Value:     12.3, Unit: "cm", UnitCode: 1, Raw: 123, Decimals: 1,
		Baud: 9600, Address: 1, Zero: 0, FullScale: 500,
	}
	var human bytes.Buffer
	if err := printMeasurement(&human, reading, false, false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(human.String(), "value=12.3 cm raw=123") {
		t.Fatalf("human output = %q", human.String())
	}

	var encoded bytes.Buffer
	if err := printMeasurement(&encoded, reading, true, false); err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Timestamp string `json:"timestamp"`
		Raw       int16  `json:"raw"`
		Unit      string `json:"unit"`
		Baud      int    `json:"baud"`
	}
	if err := json.Unmarshal(encoded.Bytes(), &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded.Timestamp != formatTimestamp(reading.Timestamp) || decoded.Raw != reading.Raw || decoded.Unit != reading.Unit || decoded.Baud != reading.Baud {
		t.Fatalf("JSON reading = %#v", decoded)
	}

	var text bytes.Buffer
	if err := printMeasurement(&text, reading, false, true); err != nil {
		t.Fatal(err)
	}
	if got, want := text.String(), formatTimestamp(reading.Timestamp)+" 12.3\n"; got != want {
		t.Fatalf("text output = %q, want %q", got, want)
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
