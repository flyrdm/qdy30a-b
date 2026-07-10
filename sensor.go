package main

import (
	"encoding/json"
	"fmt"
	"math"
	"time"

	"go.bug.st/serial"
)

var discoveryBaudRates = []int{9600, 4800, 2400, 1200, 19200, 38400, 57600, 115200}

var baudCodes = map[uint16]int{
	0: 1200,
	1: 2400,
	2: 4800,
	3: 9600,
	4: 19200,
	5: 38400,
	6: 57600,
	7: 115200,
}

var unitNames = map[uint16]string{
	0: "unitless",
	1: "cm",
	2: "mm",
	3: "MPa",
	4: "Pa",
	5: "kPa",
	6: "mA",
}

type configuration struct {
	baud    int
	address byte
	parity  serial.Parity
}

type modeSetter interface {
	SetMode(*serial.Mode) error
}

type sensor struct {
	port   serialPort
	client *modbusClient
	config configuration
}

type measurement struct {
	Timestamp time.Time `json:"timestamp"`
	Value     float64   `json:"value"`
	Unit      string    `json:"unit"`
	UnitCode  uint16    `json:"unit_code"`
	Raw       int16     `json:"raw"`
	Decimals  uint16    `json:"decimals"`
	Baud      int       `json:"baud"`
	Address   byte      `json:"address"`
	Zero      int16     `json:"zero"`
	FullScale int16     `json:"full_scale"`
}

func (m measurement) MarshalJSON() ([]byte, error) {
	type alias measurement
	return json.Marshal(struct {
		Timestamp string `json:"timestamp"`
		alias
	}{
		Timestamp: formatTimestamp(m.Timestamp),
		alias:     alias(m),
	})
}

func newSensor(port serialPort, timeout time.Duration) *sensor {
	return &sensor{port: port, client: &modbusClient{port: port, timeout: timeout}}
}

func (s *sensor) setConfiguration(config configuration) error {
	setter, ok := s.port.(modeSetter)
	if !ok {
		return fmt.Errorf("serial port cannot change communication mode")
	}
	mode := &serial.Mode{
		BaudRate: config.baud,
		DataBits: 8,
		Parity:   config.parity,
		StopBits: serial.OneStopBit,
	}
	if err := setter.SetMode(mode); err != nil {
		return fmt.Errorf("configure serial port for %d 8%s1: %w", config.baud, parityName(config.parity), err)
	}
	s.config = config
	return nil
}

func (s *sensor) probe(config configuration) error {
	if err := s.setConfiguration(config); err != nil {
		return err
	}
	response, err := s.client.readHolding(config.address, 0, 7)
	if err != nil {
		return err
	}
	actual, err := validateConfiguration(response, config.baud)
	if err != nil {
		return err
	}
	s.config.address = actual
	return nil
}

func (s *sensor) discover() (configuration, error) {
	for _, baud := range discoveryBaudRates {
		addresses := make([]byte, 0, 248)
		addresses = append(addresses, 0, 1)
		for address := 2; address <= 247; address++ {
			addresses = append(addresses, byte(address))
		}
		for _, address := range addresses {
			config := configuration{baud: baud, address: address, parity: serial.NoParity}
			if err := s.probe(config); err == nil {
				return s.config, nil
			}
		}
	}
	return configuration{}, fmt.Errorf("no QDY30A-B response at 8N1 using supported baud rates and addresses 1-247 (also tried broadcast address 0)")
}

func validateConfiguration(response holdingResponse, baud int) (byte, error) {
	if len(response.registers) != 7 {
		return 0, fmt.Errorf("configuration response contains %d registers, want 7", len(response.registers))
	}
	address := response.registers[0]
	if address < 1 || address > 247 || byte(address) != response.address {
		return 0, fmt.Errorf("implausible slave address register %d in response from %d", address, response.address)
	}
	reportedBaud, ok := baudCodes[response.registers[1]]
	if !ok || reportedBaud != baud {
		return 0, fmt.Errorf("baud register %d does not match active baud %d", response.registers[1], baud)
	}
	if response.registers[3] > 3 {
		return 0, fmt.Errorf("implausible decimal-place register %d", response.registers[3])
	}
	return byte(address), nil
}

func (s *sensor) readMeasurement() (measurement, error) {
	response, err := s.client.readHolding(s.config.address, 0, 7)
	if err != nil {
		return measurement{}, err
	}
	actualAddress, err := validateConfiguration(response, s.config.baud)
	if err != nil {
		return measurement{}, fmt.Errorf("invalid sensor registers: %w", err)
	}
	registers := response.registers
	decimals := registers[3]
	raw := int16(registers[4])
	unit, ok := unitNames[registers[2]]
	if !ok {
		unit = fmt.Sprintf("unknown(%d)", registers[2])
	}
	return measurement{
		Timestamp: time.Now(),
		Value:     float64(raw) / math.Pow10(int(decimals)),
		Unit:      unit,
		UnitCode:  registers[2],
		Raw:       raw,
		Decimals:  decimals,
		Baud:      s.config.baud,
		Address:   actualAddress,
		Zero:      int16(registers[5]),
		FullScale: int16(registers[6]),
	}, nil
}

func parityName(parity serial.Parity) string {
	switch parity {
	case serial.NoParity:
		return "N"
	case serial.EvenParity:
		return "E"
	case serial.OddParity:
		return "O"
	default:
		return "?"
	}
}
