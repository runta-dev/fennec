// reconcile-routes is an explicit diagnostic for an already paired runtime.
// It neither provisions resources nor reads API/Keychain credentials.
package main

import (
	"context"
	"encoding/json"
	"fennec/internal/agent"
	"fennec/internal/setup"
	"flag"
	"fmt"
	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
	"net"
	"os"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run() error {
	migrate := flag.Bool("migrate-model-credentials", false, "run authorized model credential migration before routing")
	runtime := flag.String("runtime", "", "explicit existing runtime ID")
	key := flag.String("identity", "", "Fennec device SSH key")
	hosts := flag.String("known-hosts", "", "verified gateway host key file")
	flag.Parse()
	if *runtime == "" || *key == "" || *hosts == "" || flag.NArg() != 0 {
		return fmt.Errorf("runtime, identity and known-hosts are required")
	}
	data, err := os.ReadFile(*key)
	if err != nil {
		return err
	}
	signer, err := ssh.ParsePrivateKey(data)
	if err != nil {
		return err
	}
	verify, err := knownhosts.New(*hosts)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	address := "ssh.runta.com:22"
	raw, err := (&net.Dialer{Timeout: 15 * time.Second}).DialContext(ctx, "tcp", address)
	if err != nil {
		return err
	}
	raw.SetDeadline(time.Now().Add(20 * time.Second))
	conn, chans, requests, err := ssh.NewClientConn(raw, address, &ssh.ClientConfig{User: *runtime, Auth: []ssh.AuthMethod{ssh.PublicKeys(signer)}, HostKeyCallback: verify})
	if err != nil {
		raw.Close()
		return err
	}
	raw.SetDeadline(time.Time{})
	client := ssh.NewClient(conn, chans, requests)
	defer client.Close()
	stop := context.AfterFunc(ctx, func() { client.Close() })
	defer stop()
	if *migrate {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		count, err := setup.MigrateMagpieCredentials(ctx, client, home)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "migrated_model_stores=%d\n", count)
	}
	versions := agent.DetectVersions(ctx)
	selected := []setup.Agent{}
	for _, v := range versions {
		if v.Error != "" {
			// Route diagnostics do not install agents. Keep a version failure
			// visible without hiding independent gateway/route evidence.
			fmt.Fprintf(os.Stderr, "version unavailable for %s; checking route only\n", v.ID)
		}
		selected = append(selected, setup.Agent{ID: v.ID, Version: v.Version})
	}
	results, err := setup.ReconcileRoutes(ctx, client, setup.LocalAgentRoutes(ctx, selected))
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(results)
}
