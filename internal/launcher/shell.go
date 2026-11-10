package launcher

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ShellInit returns an explicit opt-in initializer, never edits rc files. Existing
// interactive shells can source it; future shells can load the same block.
func ShellInit(shell, directory string) (string, error) {
	if !filepath.IsAbs(directory) || strings.ContainsAny(directory, "\x00\r\n:") {
		return "", fmt.Errorf("invalid shim path")
	}
	quote := "'" + strings.ReplaceAll(directory, "'", "'\"'\"'") + "'"
	switch shell {
	case "fish":
		fishQuote := "'" + strings.ReplaceAll(strings.ReplaceAll(directory, `\`, `\\`), "'", `\'`) + "'"
		return "# Fennec CLI integration\n" + "set -l fennec_shim_path " + fishQuote + `
set -l fennec_clean_path
for fennec_component in $PATH
    if test "$fennec_component" != "$fennec_shim_path"
        set -a fennec_clean_path "$fennec_component"
    end
end
set -gx PATH "$fennec_shim_path" $fennec_clean_path
`, nil
	case "sh", "bash", "zsh":
		return "# Fennec CLI integration\nfennec_shim_path=" + quote + `
fennec_remaining=$PATH
fennec_clean_path=
fennec_path_count=0
while :; do
  case "$fennec_remaining" in
    *:*) fennec_component=${fennec_remaining%%:*}; fennec_remaining=${fennec_remaining#*:}; fennec_more=1 ;;
    *) fennec_component=$fennec_remaining; fennec_more=0 ;;
  esac
  if [ "$fennec_component" != "$fennec_shim_path" ]; then
    if [ "$fennec_path_count" -eq 0 ]; then
      fennec_clean_path=$fennec_component
    else
      fennec_clean_path="$fennec_clean_path:$fennec_component"
    fi
    fennec_path_count=$((fennec_path_count + 1))
  fi
  [ "$fennec_more" -eq 1 ] || break
done
if [ "$fennec_path_count" -eq 0 ]; then
  export PATH="$fennec_shim_path"
else
  export PATH="$fennec_shim_path:$fennec_clean_path"
fi
unset fennec_shim_path fennec_remaining fennec_clean_path fennec_path_count fennec_component fennec_more
hash -r 2>/dev/null || true
`, nil
	default:
		return "", fmt.Errorf("unsupported shell; use sh, bash, zsh or fish")
	}
}
