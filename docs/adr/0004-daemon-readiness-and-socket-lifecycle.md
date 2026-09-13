# Daemon Readiness and Socket Lifecycle

`offbeatd` acquires its exclusive lock, opens and migrates SQLite, and only then binds an owner-only control socket. Shutdown stops new IPC work, cancels active requests, closes SQLite, removes that socket, and releases the lock; after lock acquisition, only a stale socket may be removed, while any other filesystem entry is a startup error.
