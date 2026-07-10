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

Text mode prints only the local timestamp and scaled numeric value, separated
by a space. It omits the timezone and always prints exactly six fractional
digits for microsecond precision and aligned output:

```text
2026-07-10 13:00:00.123456 12.3
```

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
