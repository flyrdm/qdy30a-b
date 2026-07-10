package main

import (
	"encoding/binary"
	"strings"
	"testing"
	"time"

	"go.bug.st/serial"
)

func (p *fakePort) SetMode(mode *serial.Mode) error {
	p.mode.baud = mode.BaudRate
	p.mode.parity = parityName(mode.Parity)
	return nil
}

func TestDiscoverSelectsRespondingConfiguration(t *testing.T) {
	port := &fakePort{}
	port.onWrite = func(port *fakePort, request []byte) {
		if request[1] != 0x03 {
			t.Fatalf("discovery wrote function 0x%02x, want read holding registers", request[1])
		}
		if port.mode.baud == 9600 && request[0] == 2 {
			port.response = responseFrame(2, 2, 3, 2, 1, 1234, 0, 10000)
		}
	}
	s := newSensor(port, time.Millisecond)
	config, err := s.discover()
	if err != nil {
		t.Fatal(err)
	}
	if config.baud != 9600 || config.address != 2 || config.parity != serial.NoParity {
		t.Fatalf("configuration = %#v", config)
	}
	if len(port.writes) != 3 {
		t.Fatalf("discovery made %d requests, want broadcast, address 1, address 2", len(port.writes))
	}
	for _, request := range port.writes {
		if binary.BigEndian.Uint16(request[2:4]) != 0 || binary.BigEndian.Uint16(request[4:6]) != 7 {
			t.Fatalf("unexpected discovery request: % x", request)
		}
	}
}

func TestMeasurementScalingAndUnknownUnit(t *testing.T) {
	port := &fakePort{}
	port.onWrite = func(port *fakePort, _ []byte) {
		port.response = responseFrame(1, 1, 3, 17, 2, 0xfb2e, 0xfff6, 5000)
	}
	s := newSensor(port, time.Millisecond)
	s.config = configuration{baud: 9600, address: 1, parity: serial.NoParity}
	reading, err := s.readMeasurement()
	if err != nil {
		t.Fatal(err)
	}
	if reading.Value != -12.34 || reading.Raw != -1234 || reading.Unit != "unknown(17)" || reading.Zero != -10 || reading.FullScale != 5000 {
		t.Fatalf("unexpected reading: %#v", reading)
	}
}

func TestValidateConfiguration(t *testing.T) {
	tests := []struct {
		name string
		resp holdingResponse
		baud int
		want string
	}{
		{name: "address mismatch", resp: holdingResponse{address: 2, registers: []uint16{1, 3, 2, 0, 0, 0, 100}}, baud: 9600, want: "slave address"},
		{name: "baud mismatch", resp: holdingResponse{address: 1, registers: []uint16{1, 2, 2, 0, 0, 0, 100}}, baud: 9600, want: "baud register"},
		{name: "decimals", resp: holdingResponse{address: 1, registers: []uint16{1, 3, 2, 4, 0, 0, 100}}, baud: 9600, want: "decimal-place"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := validateConfiguration(tt.resp, tt.baud)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}
