# Compatibility fixture status

These fixtures are deterministic, source-derived characterization targets for
the Go adapter, not captured production responses. The legacy service and
recorded live responses were unavailable during this work. `meta.took` is
excluded in the fixture comparator; response headers are tested separately.
Treat these fixtures as provisional until checked against a running legacy
backend and representative client versions.
