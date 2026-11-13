// release-components writes and checks the component manifests that let
// Fennec updates download only what changed (see internal/updater/components.go).
//
//	release-components bundle  -app Fennec.app -chromium-key KEY
//	    writes Contents/Resources/components.json before the app is signed
//	release-components verify  -app Fennec.app
//	    checks that components.json matches the bundle's content
//	release-components package -app Fennec.app -version V -arch A -out DIR [-previous FILE]
//	    writes the core and component archives and the release component manifest
//	release-components check   -app Fennec.app -version V -arch A -out DIR
//	    reassembles the bundle from those archives and compares it with the app
//	release-components tree DIR
//	    prints a directory's tree digest
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"fennec/internal/updater"
)

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "bundle":
		err = runBundle(os.Args[2:])
	case "verify":
		err = runVerify(os.Args[2:])
	case "package":
		err = runPackage(os.Args[2:], os.Stdout)
	case "check":
		err = runCheck(os.Args[2:], os.Stdout)
	case "tree":
		if len(os.Args) != 3 {
			usage()
		}
		var digest string
		digest, err = updater.TreeDigest(os.Args[2])
		if err == nil {
			fmt.Println(digest)
		}
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "release-components:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: release-components bundle|verify|package|check|tree ...")
	os.Exit(2)
}

// bundleManifest describes app's nested components as they are on disk.
func bundleManifest(app, chromiumKey string) (*updater.ComponentSet, error) {
	tree, err := updater.TreeDigest(filepath.Join(app, filepath.FromSlash(updater.ChromiumComponentPath)))
	if err != nil {
		return nil, fmt.Errorf("chromium component: %w", err)
	}
	return &updater.ComponentSet{
		Format: updater.ComponentsFormat,
		Components: []updater.Component{{
			Name: updater.ChromiumComponent,
			Path: updater.ChromiumComponentPath,
			Tree: tree,
			Key:  chromiumKey,
		}},
	}, nil
}

func runBundle(args []string) error {
	fs := flag.NewFlagSet("bundle", flag.ExitOnError)
	app := fs.String("app", "", "Fennec.app to describe")
	key := fs.String("chromium-key", "", "cache key of the signed Chromium (identity and packager sources)")
	_ = fs.Parse(args)
	if *app == "" {
		return errors.New("-app is required")
	}
	set, err := bundleManifest(*app, *key)
	if err != nil {
		return err
	}
	raw, err := json.MarshalIndent(set, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(*app, filepath.FromSlash(updater.BundleComponentsPath)), append(raw, '\n'), 0o644)
}

// verifyBundle checks that app's components.json describes its content.
func verifyBundle(app string) (*updater.ComponentSet, error) {
	sealed, err := updater.ReadBundleComponents(app)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", updater.BundleComponentsPath, err)
	}
	if _, ok := sealed.Find(updater.ChromiumComponent); !ok {
		return nil, errors.New("components.json does not list chromium")
	}
	for _, c := range sealed.Components {
		got, err := updater.TreeDigest(filepath.Join(app, filepath.FromSlash(c.Path)))
		if err != nil {
			return nil, fmt.Errorf("component %s: %w", c.Name, err)
		}
		if got != c.Tree {
			return nil, fmt.Errorf("component %s content %s does not match components.json %s", c.Name, got, c.Tree)
		}
	}
	return sealed, nil
}

func runVerify(args []string) error {
	fs := flag.NewFlagSet("verify", flag.ExitOnError)
	app := fs.String("app", "", "Fennec.app to check")
	_ = fs.Parse(args)
	if *app == "" {
		return errors.New("-app is required")
	}
	_, err := verifyBundle(*app)
	return err
}

func runPackage(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("package", flag.ExitOnError)
	app := fs.String("app", "", "signed, notarized and stapled Fennec.app")
	version := fs.String("version", "", "release version")
	arch := fs.String("arch", "arm64", "release architecture")
	outDir := fs.String("out", "", "release directory")
	previous := fs.String("previous", "", "previous release component manifest, to prove unchanged Chromium inputs kept an unchanged Chromium")
	_ = fs.Parse(args)
	if *app == "" || *version == "" || *outDir == "" {
		return errors.New("-app, -version and -out are required")
	}
	sealed, err := verifyBundle(*app)
	if err != nil {
		return err
	}
	work, err := os.MkdirTemp(filepath.Dir(filepath.Clean(*outDir)), ".release-components-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)

	release := updater.ComponentSet{Format: updater.ComponentsFormat, Version: *version, Arch: *arch}
	// Core: the bundle without its nested components, archived from a clone so
	// the signed app is never modified.
	coreApp := filepath.Join(work, "Fennec.app")
	if err := updater.CloneTree(*app, coreApp); err != nil {
		return err
	}
	for _, c := range sealed.Components {
		if err := os.RemoveAll(filepath.Join(coreApp, filepath.FromSlash(c.Path))); err != nil {
			return err
		}
	}
	coreName := updater.CoreAssetName(*version, *arch)
	core, err := archive(coreApp, filepath.Join(*outDir, coreName))
	if err != nil {
		return err
	}
	core.Name = updater.CoreComponent
	release.Components = append(release.Components, core)
	for _, c := range sealed.Components {
		name := updater.NestedAssetName(c.Name, c.Tree, *arch)
		archived, err := archive(filepath.Join(*app, filepath.FromSlash(c.Path)), filepath.Join(*outDir, name))
		if err != nil {
			return err
		}
		archived.Name, archived.Path, archived.Tree, archived.Key = c.Name, c.Path, c.Tree, c.Key
		release.Components = append(release.Components, archived)
	}
	raw, err := json.MarshalIndent(release, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if _, err := updater.ParseReleaseComponents(raw, *version, *arch); err != nil {
		return fmt.Errorf("generated manifest is invalid: %w", err)
	}
	manifestName := updater.ComponentsAssetName(*version, *arch)
	if err := os.WriteFile(filepath.Join(*outDir, manifestName), raw, 0o644); err != nil {
		return err
	}
	for _, c := range release.Components {
		fmt.Fprintf(out, "%-9s %8.1f MB  %s\n", c.Name, float64(c.Size)/1e6, c.Asset)
	}
	if *previous != "" {
		prevRaw, err := os.ReadFile(*previous)
		if err != nil {
			return err
		}
		verdict, err := comparePrevious(prevRaw, &release)
		if err != nil {
			return err
		}
		fmt.Fprintln(out, verdict)
	}
	return nil
}

// comparePrevious enforces that a nested component whose inputs did not change
// (same key) also kept its content (same tree); otherwise every user would
// download it again for nothing.
func comparePrevious(prevRaw []byte, cur *updater.ComponentSet) (string, error) {
	var prev updater.ComponentSet
	if err := json.Unmarshal(prevRaw, &prev); err != nil {
		return "", fmt.Errorf("decode previous manifest: %w", err)
	}
	var notes []string
	for _, c := range cur.Nested() {
		old, ok := prev.Find(c.Name)
		switch {
		case !ok:
			notes = append(notes, c.Name+" is new: updates download it once")
		case old.Tree == c.Tree:
			notes = append(notes, c.Name+" unchanged: updates reuse the installed copy")
		case old.Key != "" && old.Key == c.Key:
			return "", fmt.Errorf("%s was rebuilt or re-signed although its inputs (key %s) are unchanged; restore the signed copy from the release cache so updates can reuse it", c.Name, c.Key)
		default:
			notes = append(notes, c.Name+" changed (new inputs): updates download it once")
		}
	}
	return strings.Join(notes, "\n"), nil
}

// runCheck reassembles the bundle from the release directory's core and
// component archives, as an update that downloads every component would, and
// requires the result to equal app and to pass a deep strict signature check.
func runCheck(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("check", flag.ExitOnError)
	app := fs.String("app", "", "the released Fennec.app")
	version := fs.String("version", "", "release version")
	arch := fs.String("arch", "arm64", "release architecture")
	outDir := fs.String("out", "", "release directory")
	_ = fs.Parse(args)
	if *app == "" || *version == "" || *outDir == "" {
		return errors.New("-app, -version and -out are required")
	}
	raw, err := os.ReadFile(filepath.Join(*outDir, updater.ComponentsAssetName(*version, *arch)))
	if err != nil {
		return err
	}
	set, err := updater.ParseReleaseComponents(raw, *version, *arch)
	if err != nil {
		return err
	}
	work, err := os.MkdirTemp(filepath.Dir(filepath.Clean(*outDir)), ".release-components-check-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(work)
	unpack := func(c updater.Component, dest string) error {
		path := filepath.Join(*outDir, c.Asset)
		got, err := archive(path, "")
		if err != nil {
			return err
		}
		if got.Size != c.Size || got.SHA256 != c.SHA256 {
			return fmt.Errorf("%s does not match the manifest", c.Asset)
		}
		if out, err := exec.Command("/usr/bin/ditto", "-x", "-k", path, dest).CombinedOutput(); err != nil {
			return fmt.Errorf("unpack %s: %w (%s)", c.Asset, err, strings.TrimSpace(string(out)))
		}
		return nil
	}
	core, _ := set.Find(updater.CoreComponent)
	if err := unpack(core, filepath.Join(work, "core")); err != nil {
		return err
	}
	assembled := filepath.Join(work, "core", "Fennec.app")
	for _, c := range set.Nested() {
		dir := filepath.Join(work, "component-"+c.Name)
		if err := unpack(c, dir); err != nil {
			return err
		}
		unpacked := filepath.Join(dir, filepath.Base(filepath.FromSlash(c.Path)))
		if got, err := updater.TreeDigest(unpacked); err != nil || got != c.Tree {
			return fmt.Errorf("%s content does not match its tree digest", c.Asset)
		}
		if err := os.Rename(unpacked, filepath.Join(assembled, filepath.FromSlash(c.Path))); err != nil {
			return err
		}
	}
	want, err := updater.TreeDigest(*app)
	if err != nil {
		return err
	}
	got, err := updater.TreeDigest(assembled)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("reassembled bundle %s differs from the release bundle %s", got, want)
	}
	if out, err := exec.Command("/usr/bin/codesign", "--verify", "--deep", "--strict", assembled).CombinedOutput(); err != nil {
		return fmt.Errorf("reassembled bundle fails codesign: %s", strings.TrimSpace(string(out)))
	}
	fmt.Fprintf(out, "reassembled bundle matches the release bundle (tree %s) and passes codesign --verify --deep --strict\n", want[:16])
	return nil
}

// archive zips dir with ditto as the full release archive is made and returns
// its size and digest. With dest empty it only measures dir as a file.
func archive(dir, dest string) (updater.Component, error) {
	if dest != "" {
		_ = os.Remove(dest)
		if out, err := exec.Command("/usr/bin/ditto", "-c", "-k", "--sequesterRsrc", "--keepParent", dir, dest).CombinedOutput(); err != nil {
			return updater.Component{}, fmt.Errorf("archive %s: %w (%s)", filepath.Base(dir), err, strings.TrimSpace(string(out)))
		}
	} else {
		dest = dir
	}
	file, err := os.Open(dest)
	if err != nil {
		return updater.Component{}, err
	}
	defer file.Close()
	h := sha256.New()
	size, err := io.Copy(h, file)
	if err != nil {
		return updater.Component{}, err
	}
	return updater.Component{Asset: filepath.Base(dest), Size: size, SHA256: hex.EncodeToString(h.Sum(nil))}, nil
}
