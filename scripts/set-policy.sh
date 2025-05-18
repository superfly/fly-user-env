#!/bin/bash

# Set a policy to prevent all writes to /var/lib/container/data/db/ except for app.sqlite and wal/shm files
cat > /etc/apparmor.d/usr.bin.sqlite3 << EOF
#include <tunables/global>

/usr/bin/sqlite3 {
  #include <abstractions/base>
  #include <abstractions/nameservice>

  # Allow read access to all files
  /var/lib/container/data/db/** r,

  # Allow write access only to app.sqlite and wal/shm files
  /var/lib/container/data/db/app.sqlite rw,
  /var/lib/container/data/db/app.sqlite-* rw,
  /var/lib/container/data/db/app.sqlite-shm rw,
  /var/lib/container/data/db/app.sqlite-wal rw,

  # Deny all other writes
  deny /var/lib/container/data/db/** w,
}
EOF

# Reload AppArmor profiles
apparmor_parser -r /etc/apparmor.d/usr.bin.sqlite3 