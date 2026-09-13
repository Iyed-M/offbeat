# Single Daemon Owner

For each Offbeat data directory, exactly one `offbeatd` process may run. It holds an OS-level exclusive lock before opening SQLite or binding the Unix socket, preventing socket replacement and split ownership of managed state.
