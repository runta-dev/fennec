package preview

import (
	"strings"
	"testing"
)

func TestExplicitPreviewReferences(t *testing.T) {
	ref := "pv_" + strings.Repeat("a", 32)
	link := Markup(ref, "Chart [v1] *draft*")
	parts := Parse("Result:\n" + link + "\nDone.")
	if len(parts) != 3 || parts[1].Ref != ref || parts[0].Text != "Result:\n" || parts[2].Text != "Done." {
		t.Fatal(parts)
	}
	examples := []string{"```markdown\n" + link + "\n```", "~~~\n" + link + "\n~~~", "    " + link, "\t" + link, "> " + link, "`\n" + link + "\n`", link[:len(link)-1], "[Chart](fennec-preview:" + ref + ")", "![Chart](https://example.com/a.png)", "<fennec-preview version=\"1\" ref=\"" + ref + "\" />", "/root/.fen/amber-cedar/chart.png", "![Chart](fennec-preview:../../secret)"}
	for _, example := range examples {
		parts := Parse(example)
		for _, p := range parts {
			if p.Ref != "" {
				t.Fatalf("rendered example %q", example)
			}
		}
	}
}
