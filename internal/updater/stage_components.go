package updater

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// StepReuse is reported while installed components are cloned and verified.
const StepReuse = "reuse"

// byteMeter turns per-archive progress into progress over every byte this
// update actually downloads; reused components never count.
type byteMeter struct {
	base  int64
	total int64
	opts  progressOptions
}

func (m *byteMeter) child() progressOptions {
	start := m.base
	return progressOptions{
		onStep: m.opts.onStep,
		onProgress: func(done, _ int64) {
			if m.opts.onProgress != nil {
				m.opts.onProgress(start+done, m.total)
			}
		},
	}
}

// expect adds bytes that were not planned, such as a component whose reuse
// failed, so the total never falls below what is downloaded.
func (m *byteMeter) expect(n int64) { m.total += n }

func (m *byteMeter) finished(n int64) {
	m.base += n
	if m.base > m.total {
		m.total = m.base
	}
	if m.opts.onProgress != nil {
		m.opts.onProgress(m.base, m.total)
	}
}

// cloneTree copies src to dst (which must not exist) without using the
// network; tests replace it to exercise the download fallback.
var cloneTree = cloneTreeSystem

// errComponentsUnusable means the component path cannot produce a bundle and
// the full archive should be tried instead.
var errComponentsUnusable = errors.New("components unusable")

// stageComponents assembles the release bundle under root from the core
// archive plus each nested component, reused from bundle when its tree digest
// matches and downloaded otherwise. The result is root/unpack/Fennec.app; it
// is complete only when the call succeeds. reused lists the components cloned
// from bundle.
func stageComponents(ctx context.Context, transport http.RoundTripper, rel *Release, bundle, root string, plan UpdatePlan, opts progressOptions) (app string, reused []string, err error) {
	set := rel.Components
	core, ok := set.Find(CoreComponent)
	if !ok {
		return "", nil, errComponentsUnusable
	}
	meter := &byteMeter{total: plan.Download, opts: opts}
	coreAsset := componentAsset(rel, core)
	corePath := filepath.Join(root, core.Asset)
	if err := downloadVerifiedAsset(ctx, transport, coreAsset, corePath, meter.child()); err != nil {
		return "", nil, err
	}
	meter.finished(core.Size)
	if opts.onStep != nil {
		opts.onStep(StepUnpack)
	}
	unpackDir := filepath.Join(root, "unpack")
	if err := unpackArchive(ctx, corePath, unpackDir); err != nil {
		return "", nil, err
	}
	_ = os.Remove(corePath)
	app = filepath.Join(unpackDir, "Fennec.app")
	if !dirExists(app) {
		return "", nil, fmt.Errorf("%w: core archive has no Fennec.app", errComponentsUnusable)
	}

	// The core's own manifest is sealed by the release signature: it, not the
	// feed, says what goes where. The feed must agree so its archive digests
	// apply to the content the signature expects.
	sealed, err := ReadBundleComponents(app)
	if err != nil {
		return "", nil, fmt.Errorf("%w: %v", errComponentsUnusable, err)
	}
	nested := set.Nested()
	if len(sealed.Components) != len(nested) {
		return "", nil, fmt.Errorf("%w: feed and bundle disagree on components", errComponentsUnusable)
	}
	for _, c := range nested {
		want, ok := sealed.Find(c.Name)
		if !ok || want.Path != c.Path || want.Tree != c.Tree {
			return "", nil, fmt.Errorf("%w: feed and bundle disagree on %s", errComponentsUnusable, c.Name)
		}
	}

	planned := map[string]bool{}
	for _, name := range plan.Reused {
		planned[name] = true
	}
	for _, c := range nested {
		dest := filepath.Join(app, filepath.FromSlash(c.Path))
		if _, err := os.Lstat(dest); err == nil {
			return "", nil, fmt.Errorf("%w: core already contains %s", errComponentsUnusable, c.Path)
		}
		if err := mkdirNoFollow(app, filepath.Dir(filepath.FromSlash(c.Path))); err != nil {
			return "", nil, fmt.Errorf("%w: %v", errComponentsUnusable, err)
		}
		if planned[c.Name] {
			if opts.onStep != nil {
				opts.onStep(StepReuse)
			}
			if reuseComponent(ctx, filepath.Join(bundle, filepath.FromSlash(c.Path)), dest, c.Tree) {
				reused = append(reused, c.Name)
				continue
			}
			meter.expect(c.Size)
		}
		if err := downloadComponent(ctx, transport, rel, c, root, dest, meter); err != nil {
			return "", nil, err
		}
	}
	return app, reused, nil
}

// reuseComponent clones src into dest and keeps it only when the clone's tree
// digest is want. Any failure leaves dest absent.
func reuseComponent(ctx context.Context, src, dest, want string) bool {
	if !dirExists(src) || ctx.Err() != nil {
		return false
	}
	if err := cloneTree(src, dest); err != nil {
		_ = os.RemoveAll(dest)
		return false
	}
	if got, err := TreeDigest(dest); err != nil || got != want {
		_ = os.RemoveAll(dest)
		return false
	}
	clearQuarantine(dest)
	return true
}

func componentAsset(rel *Release, c Component) Asset {
	asset := rel.Assets[c.Asset]
	asset.Name = c.Asset
	asset.Size = c.Size
	asset.SHA256 = c.SHA256
	return asset
}

func downloadComponent(ctx context.Context, transport http.RoundTripper, rel *Release, c Component, root, dest string, meter *byteMeter) error {
	if _, ok := rel.Assets[c.Asset]; !ok {
		return fmt.Errorf("%w: release has no %s asset", errComponentsUnusable, c.Asset)
	}
	archive := filepath.Join(root, c.Asset)
	if err := downloadVerifiedAsset(ctx, transport, componentAsset(rel, c), archive, meter.child()); err != nil {
		return err
	}
	meter.finished(c.Size)
	if meter.opts.onStep != nil {
		meter.opts.onStep(StepUnpack)
	}
	unpackDir := filepath.Join(root, "component-"+c.Name)
	_ = os.RemoveAll(unpackDir)
	if err := unpackArchive(ctx, archive, unpackDir); err != nil {
		return err
	}
	_ = os.Remove(archive)
	unpacked := filepath.Join(unpackDir, filepath.Base(filepath.FromSlash(c.Path)))
	if got, err := TreeDigest(unpacked); err != nil || got != c.Tree {
		return fmt.Errorf("%w: %s archive content does not match its digest", errComponentsUnusable, c.Name)
	}
	if err := os.Rename(unpacked, dest); err != nil {
		return err
	}
	unregisterLaunchServices(unpacked)
	return os.RemoveAll(unpackDir)
}

// CloneTree copies the directory src to dst (which must not exist), with an
// APFS clone where possible. Release tooling uses it to split a bundle.
func CloneTree(src, dst string) error { return cloneTreeSystem(src, dst) }

// mkdirNoFollow creates rel's directories under base, refusing any existing
// element that is not a real directory, so content from an archive cannot
// redirect a component outside the staged bundle through a symlink.
func mkdirNoFollow(base, rel string) error {
	cur := base
	for _, part := range strings.Split(filepath.ToSlash(rel), "/") {
		if part == "" || part == "." {
			continue
		}
		if part == ".." {
			return fmt.Errorf("invalid component directory %s", rel)
		}
		cur = filepath.Join(cur, part)
		info, err := os.Lstat(cur)
		switch {
		case err == nil && info.IsDir():
		case err == nil:
			return fmt.Errorf("%s is not a directory", cur)
		case os.IsNotExist(err):
			if err := os.Mkdir(cur, 0o755); err != nil {
				return err
			}
		default:
			return err
		}
	}
	return nil
}
