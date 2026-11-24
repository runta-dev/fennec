package cli

import "errors"

// errSelfHostedUnavailable is returned when a paired self-hosted (BYOC) cloud
// computer is the active principal but this build has no preparation backend
// for it yet. It never reaches the Runta API.
var errSelfHostedUnavailable = errors.New("self-hosted cloud computers are not available in this version")
