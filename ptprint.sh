#!/bin/bash
DIR=$(dirname $0)
echo "/usr/bin/screen -S PTPRINT -d -m ${DIR}/ptprint/start_ptprint.sh" |/usr/bin/at now
