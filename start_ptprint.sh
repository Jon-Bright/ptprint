#!/bin/bash
DIR=$(dirname $0)
cd ${DIR}
./ptprint --dev /dev/usb/lp0
echo "ptprint exited, sleep 10s"
sleep 10
