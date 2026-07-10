package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"go.bug.st/serial"
)

type options struct {
	device   string
	baud     int
	address  int
	parity   string
	discover bool
	interval time.Duration
	timeout  time.Duration
	json     bool
	text     bool
	debug    bool
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	opts, err := parseOptions(args, stderr)
	if err != nil {
		return err
	}
	parity, err := parseParity(opts.parity)
	if err != nil {
		return err
	}

	initialBaud := opts.baud
	if initialBaud == 0 {
		initialBaud = 9600
	}
	port, err := serial.Open(opts.device, &serial.Mode{
		BaudRate: initialBaud,
		DataBits: 8,
		Parity:   parity,
		StopBits: serial.OneStopBit,
	})
	if err != nil {
		return serialOpenError(opts.device, err)
	}
	defer port.Close()

	var sensorPort serialPort = port
	if opts.debug {
		sensorPort = newWireDebugPort(port, stderr)
	}
	s := newSensor(sensorPort, opts.timeout)
	knownConfig := opts.baud != 0
	if knownConfig {
		config := configuration{baud: opts.baud, address: byte(opts.address), parity: parity}
		if err := s.probe(config); err != nil {
			return fmt.Errorf("sensor did not respond with the configured parameters (%d 8%s1, address %d): %w", config.baud, parityName(config.parity), config.address, err)
		}
	} else if opts.discover {
		fmt.Fprintf(stderr, "scanning %s with read-only Modbus requests; a complete scan may take several minutes...\n", opts.device)
		config, err := s.discover()
		if err != nil {
			return err
		}
		fmt.Fprintf(stderr, "discovered QDY30A-B: device=%s baud=%d format=8%s1 address=%d\n", opts.device, config.baud, parityName(config.parity), config.address)
	} else {
		factory := configuration{baud: 9600, address: 1, parity: serial.NoParity}
		if err := s.probe(factory); err != nil {
			fmt.Fprintf(stderr, "factory parameters did not respond; starting read-only discovery...\n")
			config, discoveryErr := s.discover()
			if discoveryErr != nil {
				return fmt.Errorf("factory probe failed (%v); discovery failed: %w", err, discoveryErr)
			}
			fmt.Fprintf(stderr, "discovered QDY30A-B: device=%s baud=%d format=8%s1 address=%d\n", opts.device, config.baud, parityName(config.parity), config.address)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	for {
		reading, err := s.readMeasurement()
		if err != nil {
			return fmt.Errorf("read sensor: %w", err)
		}
		if err := printMeasurement(stdout, reading, opts.json, opts.text); err != nil {
			return err
		}
		if opts.interval == 0 {
			return nil
		}

		timer := time.NewTimer(opts.interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func parseOptions(args []string, stderr io.Writer) (options, error) {
	var opts options
	fs := flag.NewFlagSet("qdy30a-b", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&opts.device, "device", "/dev/ttyUSB0", "serial device")
	fs.IntVar(&opts.baud, "baud", 0, "known baud rate (requires -address)")
	fs.IntVar(&opts.address, "address", 0, "known Modbus slave address (requires -baud)")
	fs.StringVar(&opts.parity, "parity", "N", "serial parity for known parameters: N, E, or O")
	fs.BoolVar(&opts.discover, "discover", false, "force read-only baud-rate and address discovery")
	fs.DurationVar(&opts.interval, "interval", 0, "polling interval; zero reads once")
	fs.DurationVar(&opts.timeout, "timeout", 250*time.Millisecond, "response timeout per request")
	fs.BoolVar(&opts.json, "json", false, "emit JSON readings")
	fs.BoolVar(&opts.text, "text", false, "emit only timestamp and numeric value")
	fs.BoolVar(&opts.debug, "debug", false, "log raw serial TX/RX bytes to stderr")
	if err := fs.Parse(args); err != nil {
		return options{}, err
	}
	if fs.NArg() != 0 {
		return options{}, fmt.Errorf("unexpected arguments: %s", strings.Join(fs.Args(), " "))
	}
	if opts.timeout <= 0 {
		return options{}, fmt.Errorf("-timeout must be positive")
	}
	if opts.interval < 0 {
		return options{}, fmt.Errorf("-interval cannot be negative")
	}
	if (opts.baud == 0) != (opts.address == 0) {
		return options{}, fmt.Errorf("-baud and -address must be provided together")
	}
	if opts.baud < 0 {
		return options{}, fmt.Errorf("-baud must be positive")
	}
	if opts.address < 0 || opts.address > 247 {
		return options{}, fmt.Errorf("-address must be between 1 and 247")
	}
	if opts.discover && opts.baud != 0 {
		return options{}, fmt.Errorf("-discover cannot be combined with -baud and -address")
	}
	if opts.baud == 0 && !strings.EqualFold(opts.parity, "N") {
		return options{}, fmt.Errorf("-parity only applies with known -baud and -address; discovery uses 8N1")
	}
	if opts.json && opts.text {
		return options{}, fmt.Errorf("-json and -text cannot be combined")
	}
	return opts, nil
}

func parseParity(value string) (serial.Parity, error) {
	switch strings.ToUpper(value) {
	case "N":
		return serial.NoParity, nil
	case "E":
		return serial.EvenParity, nil
	case "O":
		return serial.OddParity, nil
	default:
		return serial.NoParity, fmt.Errorf("invalid -parity %q: use N, E, or O", value)
	}
}

func serialOpenError(device string, err error) error {
	var portErr interface{ Code() serial.PortErrorCode }
	if errors.As(err, &portErr) {
		switch portErr.Code() {
		case serial.PermissionDenied:
			return fmt.Errorf("cannot open %s: permission denied; grant temporary access with `sudo setfacl -m u:$USER:rw %s` or add the user to dialout: %w", device, device, err)
		case serial.PortNotFound:
			return fmt.Errorf("cannot open %s: device does not exist; check the USB adapter connection: %w", device, err)
		case serial.PortBusy:
			return fmt.Errorf("cannot open %s: serial port is busy; close any program using it (including ModemManager if applicable): %w", device, err)
		}
	}
	if errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("cannot open %s: permission denied; grant temporary access with `sudo setfacl -m u:$USER:rw %s` or add the user to dialout: %w", device, device, err)
	}
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("cannot open %s: device does not exist; check the USB adapter connection: %w", device, err)
	}
	return fmt.Errorf("cannot open serial device %s: %w", device, err)
}

func printMeasurement(w io.Writer, reading measurement, asJSON, asText bool) error {
	if asJSON {
		encoder := json.NewEncoder(w)
		return encoder.Encode(reading)
	}
	if asText {
		_, err := fmt.Fprintf(w, "%s %g\n", formatTimestamp(reading.Timestamp), reading.Value)
		return err
	}
	_, err := fmt.Fprintf(w, "%s value=%g %s raw=%d decimals=%d baud=%d address=%d zero=%d full_scale=%d\n",
		formatTimestamp(reading.Timestamp), reading.Value, reading.Unit, reading.Raw,
		reading.Decimals, reading.Baud, reading.Address, reading.Zero, reading.FullScale)
	return err
}
