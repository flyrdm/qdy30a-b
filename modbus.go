package main

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"time"
)

var errResponseTimeout = errors.New("timed out waiting for Modbus response")

type serialPort interface {
	io.ReadWriteCloser
	ResetInputBuffer() error
	SetReadTimeout(time.Duration) error
}

type modbusClient struct {
	port    serialPort
	timeout time.Duration
}

type holdingResponse struct {
	address   byte
	registers []uint16
	latency   time.Duration
}

func (c *modbusClient) readHolding(address byte, start, count uint16) (holdingResponse, error) {
	if count == 0 || count > 125 {
		return holdingResponse{}, fmt.Errorf("invalid register count %d", count)
	}

	request := makeReadRequest(address, start, count)
	if err := c.port.ResetInputBuffer(); err != nil {
		return holdingResponse{}, fmt.Errorf("flush serial input: %w", err)
	}
	if err := c.port.SetReadTimeout(c.timeout); err != nil {
		return holdingResponse{}, fmt.Errorf("set serial timeout: %w", err)
	}
	started := time.Now()
	if err := writeAll(c.port, request); err != nil {
		return holdingResponse{}, fmt.Errorf("write Modbus request: %w", err)
	}

	frame, err := readResponseFrame(c.port)
	if err != nil {
		return holdingResponse{}, err
	}
	latency := time.Since(started)
	if err := validateCRC(frame); err != nil {
		return holdingResponse{}, err
	}
	if address != 0 && frame[0] != address {
		return holdingResponse{}, fmt.Errorf("Modbus response address mismatch: requested %d, received %d", address, frame[0])
	}
	if frame[1] == 0x83 {
		return holdingResponse{}, fmt.Errorf("Modbus exception 0x%02x from address %d", frame[2], frame[0])
	}
	if frame[1] != 0x03 {
		return holdingResponse{}, fmt.Errorf("unexpected Modbus function 0x%02x", frame[1])
	}
	if int(frame[2]) != int(count)*2 {
		return holdingResponse{}, fmt.Errorf("unexpected Modbus byte count %d, want %d", frame[2], count*2)
	}

	registers := make([]uint16, count)
	for i := range registers {
		registers[i] = binary.BigEndian.Uint16(frame[3+i*2 : 5+i*2])
	}
	return holdingResponse{address: frame[0], registers: registers, latency: latency}, nil
}

func makeReadRequest(address byte, start, count uint16) []byte {
	frame := make([]byte, 8)
	frame[0] = address
	frame[1] = 0x03
	binary.BigEndian.PutUint16(frame[2:4], start)
	binary.BigEndian.PutUint16(frame[4:6], count)
	crc := modbusCRC(frame[:6])
	binary.LittleEndian.PutUint16(frame[6:8], crc)
	return frame
}

func readResponseFrame(r io.Reader) ([]byte, error) {
	frame := make([]byte, 0, 256)
	buf := make([]byte, 256)
	expected := 0

	for expected == 0 || len(frame) < expected {
		n, err := r.Read(buf)
		if n > 0 {
			frame = append(frame, buf[:n]...)
			if len(frame) >= 2 && frame[1]&0x80 != 0 {
				expected = 5
			} else if len(frame) >= 3 {
				expected = 5 + int(frame[2])
				if expected > cap(buf) {
					return nil, fmt.Errorf("Modbus response is too large: %d bytes", expected)
				}
			}
			if expected > 0 && len(frame) > expected {
				return nil, fmt.Errorf("Modbus response has %d trailing bytes", len(frame)-expected)
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) && len(frame) == 0 {
				return nil, errResponseTimeout
			}
			return nil, fmt.Errorf("read Modbus response: %w", err)
		}
		if n == 0 {
			if len(frame) == 0 {
				return nil, errResponseTimeout
			}
			return nil, fmt.Errorf("incomplete Modbus response: received %d bytes", len(frame))
		}
	}
	return frame, nil
}

func validateCRC(frame []byte) error {
	if len(frame) < 5 {
		return fmt.Errorf("Modbus response is too short: %d bytes", len(frame))
	}
	want := binary.LittleEndian.Uint16(frame[len(frame)-2:])
	got := modbusCRC(frame[:len(frame)-2])
	if got != want {
		return fmt.Errorf("Modbus CRC mismatch: received 0x%04x, calculated 0x%04x", want, got)
	}
	return nil
}

func modbusCRC(data []byte) uint16 {
	crc := uint16(0xffff)
	for _, b := range data {
		crc ^= uint16(b)
		for range 8 {
			if crc&1 != 0 {
				crc = crc>>1 ^ 0xa001
			} else {
				crc >>= 1
			}
		}
	}
	return crc
}

func writeAll(w io.Writer, data []byte) error {
	for len(data) > 0 {
		n, err := w.Write(data)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		data = data[n:]
	}
	return nil
}
