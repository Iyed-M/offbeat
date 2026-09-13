# M1 Control API Scope

Milestone 1 exposes only daemon-served `status` and `config` operations. Status reports daemon identity and database readiness/schema version, while config reports sanitized effective configuration; the CLI formats both for people and does not define JSON output in this milestone.
