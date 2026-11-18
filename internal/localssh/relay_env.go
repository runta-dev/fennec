package localssh

// relayEnv marks commands the cloud computer runs on this Mac. It must equal
// launcher.RelayEnv; launcher is not imported here to keep the relay's
// dependencies small.
const relayEnv = "FENNEC_MAC_RELAY"
