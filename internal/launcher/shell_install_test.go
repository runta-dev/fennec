package launcher

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestShellInstallPreservesConfigAndIsIdempotent(t *testing.T) {
	home := t.TempDir()
	rc := filepath.Join(home, ".zshrc")
	original := "export MY_SETTING=preserved\n"
	os.WriteFile(rc, []byte(original), 0600)
	for i := 0; i < 2; i++ {
		if err := InstallShellIntegration(home, "/bin/zsh"); err != nil {
			t.Fatal(err)
		}
	}
	data, _ := os.ReadFile(rc)
	if !strings.HasPrefix(string(data), original) || strings.Count(string(data), "# >>> Fennec") != 1 {
		t.Fatal("rc changed or duplicated")
	}
	if _, err := os.Stat(filepath.Join(home, ".local/share/fennec/shell-init.sh")); err != nil {
		t.Fatal(err)
	}
}

func TestShellIntegrationHonorsZdotdir(t *testing.T) {
	home := t.TempDir()
	config := t.TempDir()
	t.Setenv("ZDOTDIR", config)
	if err := InstallShellIntegration(home, "/bin/zsh"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(config, ".zshrc")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, ".zshrc")); !os.IsNotExist(err) {
		t.Fatal("wrong rc edited")
	}
}

func TestBashIntegrationPreservesExistingLoginProfile(t *testing.T) {
	home := t.TempDir()
	profile := filepath.Join(home, ".profile")
	os.WriteFile(profile, []byte("export PROFILE_PRESERVED=yes\n"), 0600)
	for i := 0; i < 2; i++ {
		if err := InstallShellIntegration(home, "/bin/bash"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".bash_profile")); !os.IsNotExist(err) {
		t.Fatal("shadowed existing login profile")
	}
	shims := filepath.Join(home, ".local/share/fennec/shims")
	os.MkdirAll(shims, 0700)
	os.WriteFile(filepath.Join(shims, "claude"), []byte("#!/bin/sh\nexit 0\n"), 0700)
	for _, file := range []string{".bashrc", ".profile"} {
		cmd := exec.Command("bash", "--noprofile", "--norc", "-c", `. "$HOME/`+file+`"; command -v claude`)
		cmd.Env = append(os.Environ(), "HOME="+home, "PATH=/usr/bin:/bin")
		output, err := cmd.Output()
		if err != nil || strings.TrimSpace(string(output)) != filepath.Join(shims, "claude") {
			t.Fatal(file, string(output), err)
		}
	}
}

func TestZshNewInteractiveShellResolvesShim(t *testing.T) {
	shell, err := exec.LookPath("zsh")
	if err != nil {
		t.Skip("zsh unavailable")
	}
	home := t.TempDir()
	t.Setenv("ZDOTDIR", home)
	if err := InstallShellIntegration(home, shell); err != nil {
		t.Fatal(err)
	}
	shims := filepath.Join(home, ".local/share/fennec/shims")
	os.MkdirAll(shims, 0700)
	os.WriteFile(filepath.Join(shims, "claude"), []byte("#!/bin/sh\nprintf 'FENNEC_SHIM_OK'\n"), 0700)
	cmd := exec.Command(shell, "-i", "-c", "command -v claude; claude")
	cmd.Env = append(os.Environ(), "HOME="+home, "ZDOTDIR="+home, "PATH=/usr/bin:/bin")
	output, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(output), filepath.Join(shims, "claude")) || !strings.Contains(string(output), "FENNEC_SHIM_OK") {
		t.Fatalf("zsh did not load shim: %v %s", err, output)
	}
}

func TestFishIntegrationLoadsAndDeduplicates(t *testing.T) {
	fish, err := exec.LookPath("fish")
	if err != nil {
		t.Skip("fish unavailable")
	}
	home := t.TempDir()
	config := filepath.Join(home, "config")
	t.Setenv("XDG_CONFIG_HOME", config)
	for i := 0; i < 2; i++ {
		if err := InstallShellIntegration(home, fish); err != nil {
			t.Fatal(err)
		}
	}
	shims := filepath.Join(home, ".local/share/fennec/shims")
	os.MkdirAll(shims, 0700)
	os.WriteFile(filepath.Join(shims, "claude"), []byte("#!/bin/sh\necho FISH_SHIM_OK\n"), 0700)
	cmd := exec.Command(fish, "-c", `source "$XDG_CONFIG_HOME/fish/conf.d/fennec.fish"; command -s claude; claude; printf '%s\n' $PATH`)
	cmd.Env = append(os.Environ(), "HOME="+home, "XDG_CONFIG_HOME="+config, "PATH="+shims+":/usr/bin:/bin:"+shims)
	output, err := cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(output), "FISH_SHIM_OK") || strings.Count(string(output), shims+"\n") != 1 {
		t.Fatalf("fish initialization failed: %v %s", err, output)
	}
}
