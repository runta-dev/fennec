package sshcarrier

// workerCompatibilityEpoch is independent of the application build/version.
// Bump it for incompatible config, protocol or transport semantics, and for
// mandatory worker fixes that must not reuse an older process. A new epoch gets
// a separate socket/launchd job; old leases drain without being revoked.
// Keep 5 here: naming the existing contract does not change its wire format.
// See docs/transport-worker.md before changing this package.
const workerCompatibilityEpoch = 5

// The authenticated local SSH username identifies the lease wire protocol.
// A wire change also requires an epoch bump, never a fallback to an old socket.
const localLeaseUser = "fennec-transport-v1"
