# Offbeat

Offbeat maintains a local, offline representation of Spotify-organized music for one Linux user and their paired Android devices.

## Language

**Daemon owner**:
The sole active `offbeatd` process for one Offbeat data directory. It is the exclusive owner of the database and all managed state.
_Avoid_: daemon instance, server owner

**Control protocol**:
The versioned, local request-response protocol between the Offbeat CLI and its daemon over a Unix domain socket.
_Avoid_: CLI API, daemon API
