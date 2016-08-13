#!/usr/bin/env bash

nsq_tail --topic=chats --nsqd-tcp-address :4150 2> /dev/null | carton exec plackup -Ilib --listen :5004
