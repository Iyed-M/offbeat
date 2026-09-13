# Control Requests Are Not Retried

The CLI does not automatically retry a failed control request because connection loss can leave its outcome unknown. Future mutating operations must be idempotent or have durable operation identity before retry behavior is introduced.
