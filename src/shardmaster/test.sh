#!/bin/bash

for i in `seq 0 10`:
do
	go test

	if [ $? -ne 0 ]; then
		notify-send "find error"
		echo "shardmaster failed"
		echo "passed "
		echo $i
		echo " times"
		exit 1
	fi

done
