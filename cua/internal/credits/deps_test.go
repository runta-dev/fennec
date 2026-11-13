package credits

import (
	"reflect"
	"strings"
	"testing"
)

const sampleDEPS = `
# A comment.
use_relative_paths = False
git_dependencies = 'SYNC'
vars = {
  'chromium_git': 'https://chromium.googlesource.com',
  'skia_revision': Str('1111111111111111111111111111111111111111'),
  'checkout_foo': False,
  'checkout_either': 'checkout_linux or checkout_mac',
  'pkg_version': 'version:2@1.0',
}
deps = {
  'src/third_party/skia':
    Var('chromium_git') + '/skia.git' + '@' + Var('skia_revision'),
  'src/third_party/plain': 'https://example.googlesource.com/plain.git@2222222222222222222222222222222222222222',
  'src/third_party/linux_only': {
    'url': Var('chromium_git') + '/linux.git@3333333333333333333333333333333333333333',
    'condition': 'checkout_linux',
  },
  'src/third_party/mac_arm': {
    'url': '{chromium_git}/arm.git@4444444444444444444444444444444444444444',
    'condition': 'checkout_mac and (checkout_arm64 or checkout_x64) and host_os == mac and not checkout_foo',
  },
  'src/third_party/either': {
    'url': '{chromium_git}/either.git@5555555555555555555555555555555555555555',
    'condition': 'checkout_either',
  },
  'src/buildtools/mac': {
    'packages': [{'package': 'gn/gn/mac-${{arch}}', 'version': Var('pkg_version')}],
    'dep_type': 'cipd',
    'condition': 'host_os == "mac"',
  },
  'src/third_party/gcs_thing': {
    'dep_type': 'gcs',
    'bucket': 'b',
    'objects': [{'object_name': 'x', 'sha256sum': 'y', 'size_bytes': 1, 'generation': 2}],
  },
  'src/third_party/gone': None,
  'src/third_party/angle': '{chromium_git}/angle.git@6666666666666666666666666666666666666666',
}
recursedeps = [
  'src/third_party/angle',
]
hooks = [
  {
    'name': 'x',
    'pattern': '.',
    'action': ['python3', 'src/build/x.py', '--flag=' + 'y'],
  },
]
`

func TestEvalDEPS(t *testing.T) {
	f, err := evalDEPS(sampleDEPS, nil, MacPlatform("arm64"), "src")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]Dep{}
	for _, d := range f.deps {
		got[d.Path] = d
	}
	want := map[string]Dep{
		"third_party/skia":      {Path: "third_party/skia", Type: "git", URL: "https://chromium.googlesource.com/skia", Revision: strings.Repeat("1", 40)},
		"third_party/plain":     {Path: "third_party/plain", Type: "git", URL: "https://example.googlesource.com/plain", Revision: strings.Repeat("2", 40)},
		"third_party/mac_arm":   {Path: "third_party/mac_arm", Type: "git", URL: "https://chromium.googlesource.com/arm", Revision: strings.Repeat("4", 40)},
		"third_party/either":    {Path: "third_party/either", Type: "git", URL: "https://chromium.googlesource.com/either", Revision: strings.Repeat("5", 40)},
		"buildtools/mac":        {Path: "buildtools/mac", Type: "cipd", Packages: []string{"gn/gn/mac-${arch}@version:2@1.0"}},
		"third_party/gcs_thing": {Path: "third_party/gcs_thing", Type: "gcs"},
		"third_party/angle":     {Path: "third_party/angle", Type: "git", URL: "https://chromium.googlesource.com/angle", Revision: strings.Repeat("6", 40), Recurse: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("deps\n got %+v\nwant %+v", got, want)
	}
	if len(f.unevaluated) != 0 {
		t.Errorf("unevaluated %v", f.unevaluated)
	}
}

func TestEvalDEPSRelativeAndInherited(t *testing.T) {
	child := `
use_relative_paths = True
vars = { 'build_with_chromium': False, 'own': 'x' }
deps = {
  'third_party/only_standalone': { 'url': 'https://a.test/s.git@1', 'condition': 'not build_with_chromium' },
  'third_party/with_chromium': { 'url': 'https://a.test/c.git@2', 'condition': 'build_with_chromium and own == "x"' },
}`
	f, err := evalDEPS(child, map[string]any{"build_with_chromium": true}, MacPlatform("amd64"), "src/third_party/angle")
	if err != nil {
		t.Fatal(err)
	}
	if len(f.deps) != 1 || f.deps[0].Path != "third_party/angle/third_party/with_chromium" {
		t.Errorf("deps %+v", f.deps)
	}
}

func TestEvalCondition(t *testing.T) {
	vars := map[string]any{"a": true, "b": false, "os": "mac", "expr": "a and not b", "loop": "loop"}
	for cond, want := range map[string]bool{
		"a":                              true,
		"not a":                          false,
		"a and b":                        false,
		"a or b":                         true,
		"(a or b) and not b":             true,
		"os == 'mac'":                    true,
		`os == "linux"`:                  false,
		"os == mac":                      true, // an unknown name is its own string
		"os != mac":                      false,
		"expr":                           true,
		"not expr or b":                  false,
		"True and not False":             true,
		"a and (os == 'mac' or os == x)": true,
	} {
		got, err := evalCondition(cond, vars)
		if err != nil || got != want {
			t.Errorf("%q = %v, %v; want %v", cond, got, err, want)
		}
	}
	for _, bad := range []string{"a and", "(a", "a b", "os", "loop"} {
		if _, err := evalCondition(bad, vars); err == nil {
			t.Errorf("%q evaluated", bad)
		}
	}
}

func TestExpand(t *testing.T) {
	vars := map[string]any{"a": "x{b}", "b": "y", "t": true}
	for in, want := range map[string]string{"{a}-{b}": "xy-y", "${{arch}}": "${arch}", "{t}": "True", "plain": "plain"} {
		if got, err := expand(in, vars); err != nil || got != want {
			t.Errorf("%q = %q, %v", in, got, err)
		}
	}
	if _, err := expand("{missing}", vars); err == nil {
		t.Error("an unknown variable expanded")
	}
}

func TestParseDEPSErrors(t *testing.T) {
	for _, bad := range []string{"deps = {", "x = Foo('a')", "x = 'a' + True", "x = 'unterminated", "= 1"} {
		if _, err := parseDEPS(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}
