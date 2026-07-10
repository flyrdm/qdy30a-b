package main

import (
	"fmt"
	"io"
	"sync"
	"time"

	"go.bug.st/serial"
)

type configurableSerialPort interface {
	serialPort
	modeSetter
}

type wireDebugPort struct {
	port   configurableSerialPort
	output io.Writer
	mu     sync.Mutex
}

func newWireDebugPort(port configurableSerialPort, output io.Writer) *wireDebugPort {
	return &wireDebugPort{port: port, output: output}
}

func (p *wireDebugPort) Read(buf []byte) (int, error) {
	n, err := p.port.Read(buf)
	if n > 0 {
		p.log("RX", buf[:n], err)
	} else {
		p.log("RX", nil, err)
	}
	return n, err
}

func (p *wireDebugPort) Write(buf []byte) (int, error) {
	n, err := p.port.Write(buf)
	if n > 0 {
		p.log("TX", buf[:n], err)
	} else {
		p.log("TX", nil, err)
	}
	return n, err
}

func (p *wireDebugPort) Close() error {
	return p.port.Close()
}

func (p *wireDebugPort) ResetInputBuffer() error {
	return p.port.ResetInputBuffer()
}

func (p *wireDebugPort) SetReadTimeout(timeout time.Duration) error {
	return p.port.SetReadTimeout(timeout)
}

func (p *wireDebugPort) SetMode(mode *serial.Mode) error {
	err := p.port.SetMode(mode)
	p.mu.Lock()
	defer p.mu.Unlock()
	fmt.Fprintf(p.output, "%s WIRE MODE baud=%d format=8%s1", formatTimestamp(time.Now()), mode.BaudRate, parityName(mode.Parity))
	if err != nil {
		fmt.Fprintf(p.output, " error=%q", err)
	}
	fprintln(p.output)
	return err
}

func (p *wireDebugPort) log(direction string, data []byte, err error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	fmt.Fprintf(p.output, "%s WIRE %s", formatTimestamp(time.Now()), direction)
	if len(data) > 0 {
		fmt.Fprintf(p.output, " % X", data)
	} else {
		fmt.Fprint(p.output, " <no bytes>")
	}
	if err != nil {
		fmt.Fprintf(p.output, " error=%q", err)
	}
	fprintln(p.output)
}

func fprintln(w io.Writer) {
	_, _ = fmt.Fprintln(w)
}
