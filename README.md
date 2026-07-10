# QDY30A-B reader

This command reads a QDY30A-B RS485 liquid-level sensor using Modbus RTU. It
only sends function `0x03` (read holding registers); discovery never changes
the sensor's address, baud rate, units, or calibration.

## Hardware

The sensor needs a separate DC supply within the range printed on its label.
RS485 A/B carry data and do not power the sensor. Connect the sensor's A and B
lines to the USB-RS485 adapter, and follow the sensor-specific power and ground
wiring diagram. Do not assume that USB 5 V is suitable.

On Linux, grant the current login access to the adapter if necessary:

```sh
sudo setfacl -m u:$USER:rw /dev/ttyUSB0
```

This ACL is temporary and normally disappears when the USB device is
reconnected. For persistent access, add the user to `dialout` and start a new
login session.

## Usage

Build and take one reading, automatically trying the factory parameters before
falling back to read-only discovery:

```sh
go build -o qdy30a-b .
./qdy30a-b
```

Force discovery, emit JSON, or poll continuously:

```sh
./qdy30a-b -discover
./qdy30a-b -json
./qdy30a-b -text
./qdy30a-b -interval 1s
```

Text mode prints the local timestamp, scaled numeric value, and response latency
in seconds, separated by spaces. It omits the timezone and always prints exactly
six fractional digits for microsecond timestamp precision and aligned output:

```text
2026-07-10 13:00:00.123456 12.3 0.025
```

JSON output includes the same duration as `latency_seconds`. On a timeout, text
reports both the value and latency as `NaN`; JSON reports both as `null`, because
no measurement or response was received.

Log the exact serial bytes sent to and received from the adapter. Wire logs go
to stderr, leaving readings or JSON on stdout:

```sh
./qdy30a-b -baud 9600 -address 1 -debug
```

The output includes serial mode changes and each actual read/write operation:

```text
2026-07-10 13:00:00.123456 WIRE MODE baud=9600 format=8N1
2026-07-10 13:00:00.123457 WIRE TX 01 03 00 00 00 07 04 08
2026-07-10 13:00:00.157981 WIRE RX 01 03 0E ...
```

Bypass discovery when the parameters are known:

```sh
./qdy30a-b -baud 9600 -address 1 -parity N
```

Use `-device` for a different serial port and `-timeout` to adjust the response
timeout. Run `./qdy30a-b -h` for all options.

If a measurement times out, the command outputs `NaN` for that reading, closes
and reopens the serial connection, and keeps retrying. Runtime serial I/O errors
also trigger reconnection instead of terminating the polling loop.

Expose Prometheus-compatible rolling averages while polling:

```sh
./qdy30a-b -interval 1s -metrics-address 127.0.0.1:9090 \
  -metrics-name rain-water-tank-level
curl http://127.0.0.1:9090/metrics
```

Prometheus names cannot contain hyphens, so the example name is exported as
`rain_water_tank_level_average` and `rain_water_tank_level_last`. The average
gauge has a `window_seconds` label with values
`5`, `10`, `20`, `30`, `60`, and `300`. Each value is the arithmetic mean of
successful readings in that rolling window. Values are levels in centimeters.
Timed-out `NaN` readings
are excluded. The `_last` gauge reports the most recently emitted
value, including `NaN` after a timeout. Metrics without a reading are reported as
`NaN`. The default metric name prefix is `qdy30a_b_value`. The HTTP server is
disabled unless `-metrics-address` is set.

The `<name>_latency_seconds` gauge reports the most recent successful Modbus
response latency in seconds. It is measured from immediately before the read
command is written until the complete response frame has arrived. Before the
first successful response it is `NaN`.

The documented register layout is:

| Register | Meaning |
| --- | --- |
| 0 | Slave address |
| 1 | Baud-rate code |
| 2 | Unit code |
| 3 | Decimal places |
| 4 | Signed measurement |
| 5 | Signed zero point |
| 6 | Signed full-scale point |

Unknown unit codes are preserved and printed as `unknown(code)` instead of
being guessed.

## License

This project is licensed under the GNU General Public License, version 3. See
the [LICENSE](LICENSE) file for the complete license text.
