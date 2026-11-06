package handoff

type Check struct {
	ID     string `json:"id"`
	Label  string `json:"label"`
	Passed bool   `json:"passed"`
}

func RequiredChecks() []Check {
	return []Check{
		{ID: "project", Label: "Project and uncommitted changes staged"},
		{ID: "tools", Label: "Required tools and services verified"},
		{ID: "authorization", Label: "Scoped cloud authorization approved"},
		{ID: "offline", Label: "Validation passed with connector disabled"},
		{ID: "budget", Label: "Execution budget enforced"},
	}
}

func Ready(checks []Check) bool {
	for _, required := range RequiredChecks() {
		count := 0
		for _, check := range checks {
			if check.ID == required.ID {
				if !check.Passed {
					return false
				}
				count++
			}
		}
		if count != 1 {
			return false
		}
	}
	return true
}
