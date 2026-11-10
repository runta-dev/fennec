package launcher

// LocalQuery is deliberately exact: a prompt or option value containing --help
// is not a query. These invocations cannot start an agent loop.
func LocalQuery(args []string) bool {
	if len(args) != 1 {
		return false
	}
	switch args[0] {
	case "--version", "--help", "-h":
		return true
	}
	return false
}
