#!/bin/sh
# run the app with /home containing only the demo home (hides other dirs under /home)
exec unshare -rm sh -c 'mkdir -p /tmp/.demohome && mount --bind /home/brenden /tmp/.demohome && mount -t tmpfs -o mode=755 none /home && mkdir /home/brenden && mount --bind /tmp/.demohome /home/brenden && cd "$1" && exec /workspace/demo/console-places "$1"' sh "$1"
