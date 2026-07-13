#!/bin/bash

DEVICE="/dev/ttyUSB0"

sudo setfacl -m u:dvi:rw $DEVICE

while true; do

    ./qdy30a-b \
        -device $DEVICE \
        -baud 9600 \
        -address 1 \
        -parity N \
        -interval 10s \
        -text \
        -metrics-address 0.0.0.0:9091 \
        -metrics-name rain-water-tank

    sleep 10
done
