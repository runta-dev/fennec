package preview

import (
	"strings"
	"testing"
)

func TestExplicitPreviewReferences(t *testing.T) {
	ref := "pv_" + strings.Repeat("a", 32)
	link := Markup("amber-cedar", ref, "Chart [v1] *draft*")
	parts := Parse("Result:\n" + link + "\nDone.")
	if len(parts) != 3 || parts[1].Ref != ref || parts[0].Text != "Result:\n" || parts[2].Text != "Done." {
		t.Fatal(parts)
	}
	examples := []string{"```markdown\n" + link + "\n```", "~~~\n" + link + "\n~~~", "    " + link, "\t" + link, "> " + link, "`\n" + link + "\n`", link[:len(link)-1], "[Chart](fennec-preview:" + ref + ")", "![Chart](https://example.com/a.png)", "<fennec-preview version=\"1\" ref=\"" + ref + "\" />", "/root/.fen/amber-cedar/chart.png", "![Chart](fennec-preview:../../secret)",
		"[Chart](http://localhost:8080/p/amber-cedar/" + ref + ")", "[Chart](http://evil.example:47823/p/amber-cedar/" + ref + ")", "[Chart](http://localhost:47823/p/amber-cedar/" + ref + "/../x)"}
	for _, example := range examples {
		parts := Parse(example)
		for _, p := range parts {
			if p.Ref != "" {
				t.Fatalf("rendered example %q", example)
			}
		}
	}
}

// The link a terminal shows opens on the Mac; older replies used an image
// with fennec-preview:, which iOS still renders.
func TestPreviewLinkAndLegacyMarker(t *testing.T) {
	ref := "pv_" + strings.Repeat("b", 32)
	link := Markup("amber-cedar", ref, "Chart")
	if link != "[Chart](http://localhost:47823/p/amber-cedar/"+ref+")" {
		t.Fatal(link)
	}
	for _, line := range []string{link, "![Chart](fennec-preview:" + ref + ")", "![Chart](http://127.0.0.1:47823/p/amber-cedar/" + ref + ")"} {
		if parts := Parse(line); len(parts) != 1 || parts[0].Ref != ref {
			t.Fatalf("%q: %v", line, parts)
		}
	}
}
