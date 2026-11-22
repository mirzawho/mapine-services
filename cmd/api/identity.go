package main

import (
	"crypto/sha256"
	"encoding/hex"
)

// stableClientID has the legacy ObjectID JSON string shape but is an opaque
// deterministic identifier, not a MongoDB ObjectID.
func stableClientID(source, sourceID, category string) string {
	sum := sha256.Sum256([]byte(source + "\x00" + sourceID + "\x00" + category))
	return hex.EncodeToString(sum[:12])
}
