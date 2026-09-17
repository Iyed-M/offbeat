# Local Authenticated Spicetify Adapter

`offbeatd` owns one loopback-only, versioned WebSocket adapter endpoint for a single authenticated Spicetify extension session. An adapter credential provisioned outside ordinary configuration authenticates that session and is never returned through the control protocol; strict message validation and request correlation make adapter connection and snapshot requests unambiguous. M2's manually configured extension artifact is development-only; production-friendly credential provisioning and Spicetify installation/configuration remain deferred setup/packaging responsibilities.

> Scope note (2026-09-17): ADR-0010 supersedes the original roadmap's specific “Milestone 12” scheduling for production setup. The transport/authentication decision in this ADR is unchanged.
