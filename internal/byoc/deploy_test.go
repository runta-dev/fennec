package byoc_test

import (
	"fennec/internal/byoc"
	"strings"
	"testing"
)

func validDeployment(t *testing.T) byoc.Deployment {
	t.Helper()
	return byoc.Deployment{Image: byoc.DefaultImage, Container: "fennec-cloud", Volume: "fennec-home", Port: 2222, DeviceKey: byoc.AuthorizedKey(otherKey(t)) + " Fennec device"}
}

func TestDeployScriptQuotesEveryValue(t *testing.T) {
	d := validDeployment(t)
	script, err := d.Script()
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"image='ghcr.io/runta-dev/fennec-cloud:1'", "name='fennec-cloud'", "volume='fennec-home'", "port=2222", "key='" + d.DeviceKey + "'"} {
		if !strings.Contains(script, want) {
			t.Fatalf("script lacks %q", want)
		}
	}
	for _, image := range []string{"localhost:5000/fennec-cloud:test", "fennec-cloud:local", "ghcr.io/runta-dev/fennec-cloud@sha256:" + strings.Repeat("a", 64)} {
		d.Image = image
		if _, err := d.Script(); err != nil {
			t.Fatalf("rejected image %q: %v", image, err)
		}
	}
}

func TestDeployRejectsInjection(t *testing.T) {
	for name, mutate := range map[string]func(*byoc.Deployment){
		"image command":   func(d *byoc.Deployment) { d.Image = "x; rm -rf /" },
		"image quote":     func(d *byoc.Deployment) { d.Image = "x'y" },
		"image traversal": func(d *byoc.Deployment) { d.Image = "a/../b" },
		"container":       func(d *byoc.Deployment) { d.Container = "a b" },
		"volume":          func(d *byoc.Deployment) { d.Volume = "/etc" },
		"port":            func(d *byoc.Deployment) { d.Port = 0 },
		"key quote":       func(d *byoc.Deployment) { d.DeviceKey += "'; touch /tmp/x; '" },
		"key options":     func(d *byoc.Deployment) { d.DeviceKey = `command="sh" ` + d.DeviceKey },
		"key not a key":   func(d *byoc.Deployment) { d.DeviceKey = "hello" },
		"key two lines":   func(d *byoc.Deployment) { d.DeviceKey += "\n" + d.DeviceKey },
	} {
		d := validDeployment(t)
		mutate(&d)
		if _, err := d.Script(); err == nil {
			t.Fatalf("accepted %s", name)
		}
	}
}

func TestParseDeployOutput(t *testing.T) {
	key := byoc.AuthorizedKey(otherKey(t))
	got, err := byoc.ParseDeployOutput("noise\nFENNEC_HOST_KEY=" + key + " root@container\n")
	if err != nil || got != key {
		t.Fatalf("parsed %q (%v)", got, err)
	}
	for _, output := range []string{"", "FENNEC_HOST_KEY=garbage\n", "FENNEC_HOST_KEY=" + key + "\nFENNEC_HOST_KEY=" + key + "\n", `FENNEC_HOST_KEY=command="x" ` + key + "\n"} {
		if _, err := byoc.ParseDeployOutput(output); err == nil {
			t.Fatalf("accepted %q", output)
		}
	}
}
