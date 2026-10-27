#!/bin/bash
# Shortcut: tb.sh "command" runs on testbed 192.168.100.43
# For vmherd commands, auto-cd to the deploy dir
ssh -i "$HOME/.ssh/id_rsa_lenovo_bojan" -o StrictHostKeyChecking=no -o ConnectTimeout=10 bojan@192.168.100.43 \
  "cd /mnt/disk2/home/bojan/actions-runner/_work/ccattler/ccattler/test/e2e/vm-deploy && $*"
