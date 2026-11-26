package launcher

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	agents "fennec/internal/agentcmd"
)

// ShellIntegration is ShellInit followed by one function per supported agent
// and a prompt hook. In an interactive shell a function is found before any
// PATH entry, so a version manager that rebuilds PATH before each prompt
// (mise, direnv, asdf), a later PATH assignment or the command hash cannot
// bypass it. The hook moves the shims back to the front after other hooks.
//
// The code starts no process and prints nothing, works with set -u, and
// never replaces an alias or a function the user defined: aliases expand
// before functions, so an aliased agent stays the user's (fen doctor agents
// reports it). Functions use the `function name` form, whose name is not
// subject to alias expansion.
func ShellIntegration(shell, shims, dispatcher string) (string, error) {
	base, err := ShellInit(shell, shims)
	if err != nil {
		return "", err
	}
	if dispatcher == "" {
		return base, nil
	}
	if !filepath.IsAbs(dispatcher) || strings.ContainsAny(dispatcher, "\x00\r\n") {
		return "", fmt.Errorf("invalid dispatcher path")
	}
	names := agentCommandNames()
	switch shell {
	case "zsh", "bash":
		return base + posixFunctions(shell, shims, dispatcher, names), nil
	case "fish":
		return base + fishFunctions(shims, dispatcher, names), nil
	}
	return base, nil
}

// agentCommandNames includes Cursor's `agent` alias: the dispatcher runs it
// unchanged when it is not Cursor's CLI.
func agentCommandNames() []string {
	names := []string{agents.CursorAlias}
	for id := range Supported {
		names = append(names, agents.BinaryName(id))
	}
	sort.Strings(names)
	return names
}

func shQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }

func posixFunctions(shell, shims, dispatcher string, names []string) string {
	var b strings.Builder
	b.WriteString("# Fennec agent functions\n")
	b.WriteString("fennec_shims=" + shQuote(shims) + "\n")
	// Ours are recorded so a later sourcing may redefine them.
	b.WriteString("fennec_functions=${fennec_functions-}\n")
	for _, name := range names {
		var taken string
		if shell == "zsh" {
			taken = "(( ${+aliases[" + name + "]} )) || { (( ${+functions[" + name + "]} )) && case \" $fennec_functions \" in *\" " + name + " \"*) false ;; *) true ;; esac }"
		} else {
			taken = "alias " + name + " >/dev/null 2>&1 || { declare -F " + name + " >/dev/null 2>&1 && case \" $fennec_functions \" in *\" " + name + " \"*) false ;; *) true ;; esac; }"
		}
		b.WriteString("if ! { " + taken + "; }; then\n")
		b.WriteString("  function " + name + " { if [ -x " + shQuote(dispatcher) + " ]; then " + shQuote(dispatcher) + " dispatch-stable " + name + " \"$@\"; else command " + name + " \"$@\"; fi; }\n")
		b.WriteString("  case \" $fennec_functions \" in *\" " + name + " \"*) ;; *) fennec_functions=\"$fennec_functions " + name + "\" ;; esac\n")
		b.WriteString("fi\n")
	}
	// The hook keeps the shims first after hooks that rebuild PATH.
	b.WriteString(`function fennec_path_front {
  case ":${PATH-}:" in
    ":$fennec_shims:"*) return 0 ;;
  esac
  local fennec_rest="${PATH-}" fennec_clean="" fennec_part=""
  while [ -n "$fennec_rest" ]; do
    case "$fennec_rest" in
      *:*) fennec_part=${fennec_rest%%:*}; fennec_rest=${fennec_rest#*:} ;;
      *) fennec_part=$fennec_rest; fennec_rest="" ;;
    esac
    [ "$fennec_part" = "$fennec_shims" ] || fennec_clean="${fennec_clean:+$fennec_clean:}$fennec_part"
  done
  export PATH="$fennec_shims${fennec_clean:+:$fennec_clean}"
}
`)
	if shell == "zsh" {
		b.WriteString(`function fennec_precmd {
  fennec_path_front
  precmd_functions=(${precmd_functions:#fennec_precmd} fennec_precmd)
}
precmd_functions=(${precmd_functions:#fennec_precmd} fennec_precmd)
`)
	} else {
		b.WriteString(`case ";${PROMPT_COMMAND-};" in
  *";fennec_path_front;"*) ;;
  *) PROMPT_COMMAND="${PROMPT_COMMAND:+$PROMPT_COMMAND;}fennec_path_front" ;;
esac
`)
	}
	return b.String()
}

func fishQuote(s string) string {
	return "'" + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), "'", `\'`) + "'"
}

func fishFunctions(shims, dispatcher string, names []string) string {
	var b strings.Builder
	b.WriteString("# Fennec agent functions\n")
	for _, name := range names {
		b.WriteString("if not functions -q " + name + "; or set -q fennec_function_" + strings.ReplaceAll(name, "-", "_") + "\n")
		b.WriteString("    function " + name + "\n")
		b.WriteString("        if test -x " + fishQuote(dispatcher) + "\n")
		b.WriteString("            " + fishQuote(dispatcher) + " dispatch-stable " + name + " $argv\n")
		b.WriteString("        else\n            command " + name + " $argv\n        end\n    end\n")
		b.WriteString("    set -g fennec_function_" + strings.ReplaceAll(name, "-", "_") + " 1\n")
		b.WriteString("end\n")
	}
	b.WriteString("function fennec_path_front --on-event fish_prompt\n")
	b.WriteString("    set -l fennec_shims " + fishQuote(shims) + "\n")
	b.WriteString("    test \"$PATH[1]\" = \"$fennec_shims\"; and return\n")
	b.WriteString("    set -l fennec_clean\n")
	b.WriteString("    for fennec_part in $PATH\n        test \"$fennec_part\" != \"$fennec_shims\"; and set -a fennec_clean $fennec_part\n    end\n")
	b.WriteString("    set -gx PATH $fennec_shims $fennec_clean\n")
	b.WriteString("end\n")
	return b.String()
}
