package main

import (
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"strings"
	"testing"
	"time"
)

type fakePort struct {
	mode       serialMode
	writes     [][]byte
	response   []byte
	chunks     []int
	readError  error
	onWrite    func(*fakePort, []byte)
	closeCount int
}

type serialMode struct {
	baud   int
	parity string
}

func (p *fakePort) Read(buf []byte) (int, error) {
	if len(p.response) == 0 {
		if p.readError != nil {
			err := p.readError
			p.readError = nil
			return 0, err
		}
		return 0, nil
	}
	n := len(p.response)
	if len(p.chunks) > 0 {
		n = p.chunks[0]
		p.chunks = p.chunks[1:]
	}
	if n > len(p.response) {
		n = len(p.response)
	}
	if n > len(buf) {
		n = len(buf)
	}
	copy(buf, p.response[:n])
	p.response = p.response[n:]
	return n, nil
}

func (p *fakePort) Write(data []byte) (int, error) {
	copyOfData := append([]byte(nil), data...)
	p.writes = append(p.writes, copyOfData)
	if p.onWrite != nil {
		p.onWrite(p, copyOfData)
	}
	return len(data), nil
}

func (p *fakePort) Close() error {
	p.closeCount++
	return nil
}
func (p *fakePort) ResetInputBuffer() error            { return nil }
func (p *fakePort) SetReadTimeout(time.Duration) error { return nil }

func TestReadRequestCRC(t *testing.T) {
	got := makeReadRequest(1, 4, 1)
	want := []byte{0x01, 0x03, 0x00, 0x04, 0x00, 0x01, 0xc5, 0xcb}
	if !bytes.Equal(got, want) {
		t.Fatalf("request = % x, want % x", got, want)
	}
}

func TestReadHoldingFragmentedResponse(t *testing.T) {
	port := &fakePort{
		response: responseFrame(1, 0x1234, 0xfffe),
		chunks:   []int{1, 2, 1, 3, 2},
	}
	client := modbusClient{port: port, timeout: time.Millisecond}
	got, err := client.readHolding(1, 4, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got.address != 1 || len(got.registers) != 2 || got.registers[0] != 0x1234 || got.registers[1] != 0xfffe {
		t.Fatalf("unexpected response: %#v", got)
	}
	if got.latency <= 0 {
		t.Fatalf("latency = %s, want a positive duration", got.latency)
	}
}

func TestReadHoldingErrors(t *testing.T) {
	tests := []struct {
		name     string
		response []byte
		address  byte
		want     string
	}{
		{name: "timeout", want: "timed out"},
		{name: "crc", response: []byte{1, 3, 2, 0, 1, 0, 0}, address: 1, want: "CRC mismatch"},
		{name: "address", response: responseFrame(2, 1), address: 1, want: "address mismatch"},
		{name: "exception", response: exceptionFrame(1, 2), address: 1, want: "exception 0x02"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			port := &fakePort{response: tt.response}
			client := modbusClient{port: port, timeout: time.Millisecond}
			_, err := client.readHolding(tt.address, 4, 1)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want substring %q", err, tt.want)
			}
		})
	}
}

func TestReadResponseIOError(t *testing.T) {
	want := errors.New("device disconnected")
	port := &fakePort{readError: want}
	client := modbusClient{port: port, timeout: time.Millisecond}
	_, err := client.readHolding(1, 4, 1)
	if !errors.Is(err, want) {
		t.Fatalf("error = %v, want wrapped %v", err, want)
	}
}

func TestReadResponseRejectsIncompleteFrame(t *testing.T) {
	port := &fakePort{response: []byte{1, 3, 2, 0}}
	client := modbusClient{port: port, timeout: time.Millisecond}
	_, err := client.readHolding(1, 4, 1)
	if err == nil || !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("error = %v, want incomplete response", err)
	}
}

func responseFrame(address byte, registers ...uint16) []byte {
	frame := make([]byte, 3+len(registers)*2+2)
	frame[0] = address
	frame[1] = 3
	frame[2] = byte(len(registers) * 2)
	for i, register := range registers {
		binary.BigEndian.PutUint16(frame[3+i*2:], register)
	}
	binary.LittleEndian.PutUint16(frame[len(frame)-2:], modbusCRC(frame[:len(frame)-2]))
	return frame
}

func exceptionFrame(address, code byte) []byte {
	frame := []byte{address, 0x83, code, 0, 0}
	binary.LittleEndian.PutUint16(frame[3:], modbusCRC(frame[:3]))
	return frame
}

var _ io.ReadWriteCloser = (*fakePort)(nil)
