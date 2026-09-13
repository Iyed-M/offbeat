# Daemon-Served Effective Configuration

`offbeat config` reports the daemon's active resolved configuration through the control protocol, with secrets redacted. The CLI may read local configuration only to discover the socket; it never renders a potentially divergent file configuration as daemon state.
