// Command cua-vm manages the tart macOS VM that fennec-cua's desktop tests run
// in. tart is a development dependency: brew install cirruslabs/cli/tart.
//
//	go run ./cmd/cua-vm create [-image IMAGE]   clone the VM (first run downloads the image)
//	go run ./cmd/cua-vm up                      boot it headless and wait for SSH
//	go run ./cmd/cua-vm down                    shut it down
//	go run ./cmd/cua-vm exec -- COMMAND         run a shell command in the VM
//	go run ./cmd/cua-vm gui -- COMMAND          run it in the VM's desktop session
//	go run ./cmd/cua-vm grant CLIENT            grant Accessibility and Screen Recording
//	go run ./cmd/cua-vm test [-tags T] PKG...   build test binaries, run them in the VM's desktop session
//	go run ./cmd/cua-vm fixture                 build the fixture app and install it in the VM
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"

	"fennec/cua/internal/vm"
)

// Where artifacts live in the VM.
const guestDir = "/Users/admin/cua"

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "cua-vm:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: cua-vm create | up | down | exec | gui | grant | test | fixture")
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	v := vm.New()
	if name := os.Getenv("CUA_VM"); name != "" {
		v.Name = name
	}
	rest := args[1:]
	switch args[0] {
	case "create":
		fs := flag.NewFlagSet("create", flag.ExitOnError)
		image := fs.String("image", vm.DefaultImage, "tart image to clone")
		_ = fs.Parse(rest)
		fmt.Printf("cloning %s into %s (the first run downloads the image)\n", *image, v.Name)
		return v.Create(ctx, *image)
	case "up":
		ip, err := v.Start(ctx)
		if err == nil {
			fmt.Println(ip)
		}
		return err
	case "down":
		return v.Stop(ctx)
	case "exec", "gui":
		command := strings.Join(trimDashes(rest), " ")
		if command == "" {
			return errors.New("no command")
		}
		if args[0] == "gui" {
			command = vm.InGUI(command)
		}
		return withSession(ctx, v, func(s *vm.Session) error {
			return s.Run(ctx, command, os.Stdin, os.Stdout, os.Stderr)
		})
	case "grant":
		if len(rest) != 1 {
			return errors.New("grant takes one bundle id or absolute path")
		}
		return withSession(ctx, v, func(s *vm.Session) error { return s.GrantTCC(ctx, rest[0]) })
	case "fixture":
		return fixture(ctx, v)
	case "test":
		return test(ctx, v, rest)
	}
	return fmt.Errorf("unknown command %q", args[0])
}

func trimDashes(args []string) []string {
	if len(args) > 0 && args[0] == "--" {
		return args[1:]
	}
	return args
}

func withSession(ctx context.Context, v *vm.VM, f func(*vm.Session) error) error {
	if _, err := v.Start(ctx); err != nil {
		return err
	}
	s, err := v.Connect(ctx)
	if err != nil {
		return err
	}
	defer s.Close()
	return f(s)
}

func moduleRoot() (string, error) {
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		return "", err
	}
	return filepath.Dir(strings.TrimSpace(string(out))), nil
}

// fixture builds CUAFixture on the host and installs it in the VM.
func fixture(ctx context.Context, v *vm.VM) error {
	root, err := moduleRoot()
	if err != nil {
		return err
	}
	build := exec.CommandContext(ctx, "bash", filepath.Join(root, "testapp", "build.sh"))
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	if err := build.Run(); err != nil {
		return fmt.Errorf("build fixture: %w", err)
	}
	return withSession(ctx, v, func(s *vm.Session) error {
		if err := pushDir(ctx, s, filepath.Join(root, "testapp", "build"), "CUAFixture.app", guestDir+"/apps"); err != nil {
			return err
		}
		return s.GrantTCC(ctx, "com.runta.fennec.cua-fixture")
	})
}

// pushDir pushes dir/name to dest in the VM.
func pushDir(ctx context.Context, s *vm.Session, dir, name, dest string) error {
	wd, err := os.Getwd()
	if err != nil {
		return err
	}
	if err := os.Chdir(dir); err != nil {
		return err
	}
	defer func() { _ = os.Chdir(wd) }()
	return s.Push(ctx, dest, name)
}

// test cross-builds test binaries for the VM, pushes them and runs them in
// the VM's desktop session, each with the given -test flags after "--".
func test(ctx context.Context, v *vm.VM, args []string) error {
	fs := flag.NewFlagSet("test", flag.ExitOnError)
	tags := fs.String("tags", "", "build tags, such as axlive or fixture")
	_ = fs.Parse(args)
	pkgs := fs.Args()
	var testFlags []string
	for i, a := range pkgs {
		if a == "--" {
			pkgs, testFlags = pkgs[:i], pkgs[i+1:]
			break
		}
	}
	if len(pkgs) == 0 {
		return errors.New("name at least one package")
	}
	root, err := moduleRoot()
	if err != nil {
		return err
	}
	stage, err := os.MkdirTemp("", "cua-vm-test")
	if err != nil {
		return err
	}
	defer os.RemoveAll(stage)
	var bins []string
	for _, pkg := range pkgs {
		name := strings.ReplaceAll(strings.Trim(pkg, "./"), "/", "_") + ".test"
		cmd := exec.CommandContext(ctx, "go", "test", "-c", "-o", filepath.Join(stage, name), "-tags", *tags, pkg)
		cmd.Dir = root
		cmd.Env = append(os.Environ(), "GOOS=darwin", "GOARCH=arm64", "CGO_ENABLED=1")
		cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
		if err := cmd.Run(); err != nil {
			return fmt.Errorf("build %s: %w", pkg, err)
		}
		bins = append(bins, name)
	}
	return withSession(ctx, v, func(s *vm.Session) error {
		if err := pushDir(ctx, s, stage, ".", guestDir+"/tests"); err != nil {
			return err
		}
		failed := 0
		for _, b := range bins {
			fmt.Printf("=== %s in the VM desktop session\n", b)
			command := "cd " + vm.Quote(guestDir+"/tests") + " && ./" + b + " -test.v " + strings.Join(quoteAll(testFlags), " ")
			if err := s.Run(ctx, vm.InGUI(command), nil, os.Stdout, os.Stderr); err != nil {
				failed++
				fmt.Printf("--- %s failed: %v\n", b, err)
			}
		}
		if failed > 0 {
			return fmt.Errorf("%d of %d test binaries failed", failed, len(bins))
		}
		return nil
	})
}

func quoteAll(ss []string) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = vm.Quote(s)
	}
	return out
}
