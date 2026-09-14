# Local Authenticated Spicetify Adapter

`offbeatd` owns one loopback-only, versioned WebSocket adapter endpoint for a single authenticated Spicetify extension session. An adapter credential provisioned outside ordinary configuration authenticates that session and is never returned through the control protocol; strict message validation and request correlation make adapter connection and snapshot requests unambiguous. Production credential generation and Spicetify installation remain `offbeat setup` responsibilities.
