// fennec-cloud is the entry point of the Fennec self-hosted cloud image.
//
//	fennec-cloud init                  prepare the volume, print pairing details, run sshd
//	fennec-cloud authorize <key>       authorize a device public key
//	fennec-cloud revoke <key|SHA256:…> revoke a device
//	fennec-cloud devices               list authorized devices
//	fennec-cloud fingerprint           print the host key fingerprint
//	fennec-cloud identity              print the machine ID
package main

import (
	"errors"
	"fennec/internal/cloudimage"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"

	"golang.org/x/crypto/ssh"
)

const sshd = "/usr/sbin/sshd"

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "fennec-cloud:", err)
		os.Exit(1)
	}
}

func run(args []string, output io.Writer) error {
	layout := cloudimage.DefaultLayout()
	if len(args) == 0 {
		return usage()
	}
	switch command, rest := args[0], args[1:]; {
	case command == "init" && len(rest) == 0:
		return initialize(layout, output)
	case command == "authorize" && len(rest) >= 1:
		added, err := cloudimage.Authorize(layout, strings.Join(rest, " "))
		if err != nil {
			return err
		}
		if added {
			fmt.Fprintln(output, "Device authorized.")
		} else {
			fmt.Fprintln(output, "Device was already authorized.")
		}
		return nil
	case command == "revoke" && len(rest) >= 1:
		removed, err := cloudimage.Revoke(layout, strings.Join(rest, " "))
		if err != nil {
			return err
		}
		if !removed {
			return errors.New("no authorized device matches")
		}
		fmt.Fprintln(output, "Device revoked. Its existing connections end when they disconnect.")
		return nil
	case command == "devices" && len(rest) == 0:
		devices, err := cloudimage.Devices(layout)
		if err != nil {
			return err
		}
		for _, line := range devices {
			key, comment, _, _, err := ssh.ParseAuthorizedKey([]byte(line))
			if err == nil {
				fmt.Fprintf(output, "%s %s\n", ssh.FingerprintSHA256(key), comment)
			}
		}
		return nil
	case command == "fingerprint" && len(rest) == 0:
		fingerprint, err := cloudimage.Fingerprint(layout)
		if err != nil {
			return err
		}
		fmt.Fprintln(output, fingerprint)
		return nil
	case command == "identity" && len(rest) == 0:
		machine, err := cloudimage.MachineID(layout)
		if err != nil {
			return err
		}
		fmt.Fprintln(output, machine)
		return nil
	}
	return usage()
}

func usage() error {
	return errors.New("usage: fennec-cloud init | authorize <public-key> | revoke <public-key|SHA256:fingerprint> | devices | fingerprint | identity")
}

func initialize(layout cloudimage.Layout, output io.Writer) error {
	if os.Geteuid() != 0 {
		return errors.New("init must run as root (it is the image entry point)")
	}
	report, err := cloudimage.Init(layout, os.Getenv("FENNEC_AUTHORIZED_KEY"))
	if err != nil {
		return err
	}
	// sshd's privilege separation directory.
	if err := os.MkdirAll("/run/sshd", 0755); err != nil {
		return err
	}
	fmt.Fprint(output, banner(report))
	environment := []string{}
	for _, entry := range os.Environ() {
		if !strings.HasPrefix(entry, "FENNEC_AUTHORIZED_KEY=") {
			environment = append(environment, entry)
		}
	}
	// sshd re-executes itself for each connection and needs its absolute path.
	return syscall.Exec(sshd, []string{sshd, "-D", "-e"}, environment)
}

func banner(report cloudimage.Report) string {
	var b strings.Builder
	b.WriteString("Fennec cloud computer is ready.\n")
	fmt.Fprintf(&b, "  Machine ID:           %s\n", report.MachineID)
	fmt.Fprintf(&b, "  Host key fingerprint: %s\n", report.Fingerprint)
	fmt.Fprintf(&b, "  Authorized devices:   %d\n", report.Devices)
	if report.Devices == 0 {
		b.WriteString("\nNo device is authorized yet. On your Mac run `fen cloud byoc key`, then:\n")
		b.WriteString("  docker exec <container> fennec-cloud authorize '<that key>'\n")
	}
	b.WriteString("\nPair from your Mac:\n")
	fmt.Fprintf(&b, "  fen cloud byoc pair <this host>:<published port> --fingerprint %s\n", report.Fingerprint)
	return b.String()
}
