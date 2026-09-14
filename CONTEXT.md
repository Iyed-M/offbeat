# Offbeat

Offbeat maintains a local, offline representation of Spotify-organized music for one Linux user and their paired Android devices.

## Language

**Daemon owner**:
The sole active `offbeatd` process for one Offbeat data directory. It is the exclusive owner of the database and all managed state.
_Avoid_: daemon instance, server owner

**Control protocol**:
The versioned, local request-response protocol between the Offbeat CLI and its daemon over a Unix domain socket.
_Avoid_: CLI API, daemon API

**Adapter credential**:
The random secret that authenticates the local Spicetify extension to its daemon. It is distinct from a Spotify credential and is never exposed through Offbeat's control protocol.
_Avoid_: Spotify token, API key

**Adapter endpoint**:
The loopback-only, versioned WebSocket address at which the daemon accepts an adapter session.
_Avoid_: Spotify endpoint, LAN endpoint

**Adapter session**:
The single authenticated WebSocket connection from the Spicetify extension to the daemon. It is connected or disconnected and carries daemon-issued snapshot requests.
_Avoid_: Spotify connection, adapter instance

**Snapshot request**:
A daemon-issued, uniquely correlated request for the connected adapter to produce one Spotify snapshot. A response belongs only to the request carrying the same correlation identifier.
_Avoid_: sync event, adapter push
