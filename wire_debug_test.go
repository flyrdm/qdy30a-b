package main

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"go.bug.st/serial"
)

func TestWireDebugLogsActualReadWriteChunks(t *testing.T) {
	port := &fakePort{
		response: responseFrame(1, 123),
		chunks:   []int{2, 5},
	}
	var log bytes.Buffer
	debugPort := newWireDebugPort(port, &log)
	if err := debugPort.SetMode(&serial.Mode{BaudRate: 9600, DataBits: 8, Parity: serial.NoParity, StopBits: serial.OneStopBit}); err != nil {
		t.Fatal(err)
	}
	client := modbusClient{port: debugPort, timeout: time.Millisecond}
	if _, err := client.readHolding(1, 4, 1); err != nil {
		t.Fatal(err)
	}

	output := log.String()
	for _, want := range []string{
		"WIRE MODE baud=9600 format=8N1",
		"WIRE TX 01 03 00 04 00 01 C5 CB",
		"WIRE RX 01 03",
		"WIRE RX 02 00 7B",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("wire log does not contain %q:\n%s", want, output)
		}
	}
}
