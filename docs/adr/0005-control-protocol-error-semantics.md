# Control Protocol Error Semantics

Daemon failures use a versioned structured error with one of `invalid_request`, `unsupported_version`, `failed_precondition`, or `internal`; successful replies contain a result instead. The CLI reports its own connection failures as daemon-unavailable errors with exit code 1, reserves exit code 2 for invalid CLI usage, and returns 0 on success.
