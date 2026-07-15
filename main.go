package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"math"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"go.bug.st/serial"
)

type options struct {
	device            string
	baud              int
	address           int
	parity            string
	discover          bool
	interval          time.Duration
	timeout           time.Duration
	json              bool
	text              bool
	debug             bool
	metricsAddress    string
	metricsName       string
	metricsRetainLast bool
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		_ = writeTimestamped(os.Stderr, time.Now(), "error: %v\n", err)
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
	port, err := openSerialPort(opts.device, initialBaud, parity)
	if err != nil {
		return serialOpenError(opts.device, err)
	}

	var sensorPort serialPort = port
	if opts.debug {
		sensorPort = newWireDebugPort(port, stderr)
	}
	s := newSensor(sensorPort, opts.timeout)
	defer func() { _ = s.port.Close() }()
	knownConfig := opts.baud != 0
	if knownConfig {
		config := configuration{baud: opts.baud, address: byte(opts.address), parity: parity}
		if err := s.probe(config); err != nil {
			return fmt.Errorf("sensor did not respond with the configured parameters (%d 8%s1, address %d): %w", config.baud, parityName(config.parity), config.address, err)
		}
	} else if opts.discover {
		_ = writeTimestamped(stderr, time.Now(), "scanning %s with read-only Modbus requests; a complete scan may take several minutes...\n", opts.device)
		config, err := s.discover()
		if err != nil {
			return err
		}
		printDetectedConfiguration(stderr, opts.device, config)
	} else {
		factory := configuration{baud: 9600, address: 1, parity: serial.NoParity}
		config := factory
		if err := s.probe(factory); err != nil {
			_ = writeTimestamped(stderr, time.Now(), "factory parameters did not respond; starting read-only discovery...\n")
			discovered, discoveryErr := s.discover()
			if discoveryErr != nil {
				return fmt.Errorf("factory probe failed (%v); discovery failed: %w", err, discoveryErr)
			}
			config = discovered
		}
		printDetectedConfiguration(stderr, opts.device, config)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	averages := newRollingAverages()
	if opts.metricsAddress != "" {
		metricName, err := normalizeMetricName(opts.metricsName)
		if err != nil {
			return err
		}
		listener, err := net.Listen("tcp", opts.metricsAddress)
		if err != nil {
			return fmt.Errorf("listen for metrics on %s: %w", opts.metricsAddress, err)
		}
		server := &http.Server{
			Handler: metricsHandler{
				averages:   averages,
				name:       metricName,
				retainLast: opts.metricsRetainLast,
				log:        stderr,
			},
			ReadHeaderTimeout: 5 * time.Second,
			ErrorLog:          log.New(timestampWriter{output: stderr}, "", 0),
		}
		defer server.Close()
		go func() {
			if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
				_ = writeTimestamped(stderr, time.Now(), "metrics server: %v\n", err)
			}
		}()
		_ = writeTimestamped(stderr, time.Now(), "serving %s metrics at http://%s/metrics\n", metricName, listener.Addr())
	}
	var lastReading measurement
	for {
		reading, err := s.readMeasurement()
		if err != nil {
			if errors.Is(err, errResponseTimeout) {
				reading = unavailableMeasurement(s.config, lastReading)
				averages.observe(reading)
				if err := printMeasurement(stdout, reading, opts.json, opts.text); err != nil {
					return err
				}
				_ = writeTimestamped(stderr, time.Now(), "read sensor: %v; output NaN and reconnecting\n", err)
			} else {
				averages.markUnavailable()
				_ = writeTimestamped(stderr, time.Now(), "read sensor: %v; reconnecting\n", err)
			}
			_ = s.port.Close()
			if !waitForNextRead(ctx, opts.interval) {
				return nil
			}
			s, err = reconnectSensor(ctx, opts, s.config, stderr)
			if err != nil {
				return err
			}
			if s == nil {
				return nil
			}
			continue
		}
		lastReading = reading
		averages.observe(reading)
		if err := printMeasurement(stdout, reading, opts.json, opts.text); err != nil {
			return err
		}
		if opts.interval == 0 {
			return nil
		}
		if !waitForNextRead(ctx, opts.interval) {
			return nil
		}
	}
}

func printDetectedConfiguration(output io.Writer, device string, config configuration) {
	parity := parityName(config.parity)
	now := time.Now()
	_ = writeTimestamped(output, now, "detected QDY30A-B: device=%s baud=%d format=8%s1 address=%d\n", device, config.baud, parity, config.address)
	_ = writeTimestamped(output, now, "launch with: ./qdy30a-b -device %s -baud %d -address %d -parity %s\n", device, config.baud, config.address, parity)
}

var openSerialPort = func(device string, baud int, parity serial.Parity) (configurableSerialPort, error) {
	return serial.Open(device, &serial.Mode{
		BaudRate: baud,
		DataBits: 8,
		Parity:   parity,
		StopBits: serial.OneStopBit,
	})
}

func reconnectSensor(ctx context.Context, opts options, config configuration, stderr io.Writer) (*sensor, error) {
	retryDelay := opts.interval
	if retryDelay <= 0 || retryDelay > time.Second {
		retryDelay = time.Second
	}
	for {
		port, err := openSerialPort(opts.device, config.baud, config.parity)
		if err == nil {
			var sensorPort serialPort = port
			if opts.debug {
				sensorPort = newWireDebugPort(port, stderr)
			}
			s := newSensor(sensorPort, opts.timeout)
			if err = s.setConfiguration(config); err == nil {
				_ = writeTimestamped(stderr, time.Now(), "reconnected to %s\n", opts.device)
				return s, nil
			}
			_ = port.Close()
			err = fmt.Errorf("configure sensor connection: %w", err)
		} else {
			err = serialOpenError(opts.device, err)
		}
		_ = writeTimestamped(stderr, time.Now(), "reconnect failed: %v; retrying in %s\n", err, retryDelay)
		if !waitForNextRead(ctx, retryDelay) {
			return nil, nil
		}
	}
}

func waitForNextRead(ctx context.Context, delay time.Duration) bool {
	if delay <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
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
	fs.StringVar(&opts.metricsAddress, "metrics-address", "", "HTTP address for /metrics (disabled when empty)")
	fs.StringVar(&opts.metricsName, "metrics-name", "qdy30a_b_value", "Prometheus metric name prefix")
	fs.BoolVar(&opts.metricsRetainLast, "metrics-retain-last", true, "retain the last successful metric values while the sensor is unavailable")
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
		if !reading.LatencyValid {
			_, err := fmt.Fprintf(w, "%s\tlevel_cm=%g\tread_latency_seconds=NaN\n", formatTimestamp(reading.Timestamp), reading.Value)
			return err
		}
		_, err := fmt.Fprintf(w, "%s\tlevel_cm=%g\tread_latency_seconds=%g\n", formatTimestamp(reading.Timestamp), reading.Value, reading.Latency.Seconds())
		return err
	}
	latencySeconds := math.NaN()
	if reading.LatencyValid {
		latencySeconds = reading.Latency.Seconds()
	}
	_, err := fmt.Fprintf(w, "%s value=%g %s latency_seconds=%g raw=%d decimals=%d baud=%d address=%d zero=%d full_scale=%d\n",
		formatTimestamp(reading.Timestamp), reading.Value, reading.Unit, latencySeconds, reading.Raw,
		reading.Decimals, reading.Baud, reading.Address, reading.Zero, reading.FullScale)
	return err
}
