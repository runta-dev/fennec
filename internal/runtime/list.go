package runtime

// Compatibility entrypoint; the daemon uses LocalRuntime.List on its resident channel.
const ListScript = "exec \"$HOME/.local/fennec/bin/fennec-terminal\" list\n"
