package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"fennec/internal/cloudarchive"
)

// cloudArchiveCommand is `fen cloud export|import|inspect`, the command-line
// form of the Settings › Cloud computer export and import (THI-165).
func cloudArchiveCommand(ctx context.Context, client cloudCaller, args []string, output, progress io.Writer, poll time.Duration) error {
	usage := fmt.Errorf("usage: fen cloud export [--no-sessions] [--output <file>] | import <file> | inspect <file>")
	switch args[0] {
	case "inspect", "import":
		if len(args) != 2 {
			return usage
		}
		path, err := filepath.Abs(args[1])
		if err != nil {
			return err
		}
		var manifest cloudarchive.Manifest
		if err := client.Call(ctx, "cloud.archive.inspect", map[string]string{"path": path}, &manifest); err != nil {
			return err
		}
		if args[0] == "inspect" {
			return json.NewEncoder(output).Encode(manifest)
		}
		fmt.Fprintf(progress, "Importing %d requests and %d sessions exported %s\n", manifest.Counts.Requests, manifest.Counts.Sessions, manifest.Created.Local().Format("2006-01-02 15:04"))
		job, err := runArchiveJob(ctx, client, "cloud.archive.import", map[string]string{"path": path}, progress, poll)
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(job.Result)
	case "export":
		params := map[string]bool{"sessions": true}
		destination := ""
		for i := 1; i < len(args); i++ {
			switch {
			case args[i] == "--no-sessions":
				params["sessions"] = false
			case args[i] == "--output" && i+1 < len(args):
				i++
				destination = args[i]
			default:
				return usage
			}
		}
		job, err := runArchiveJob(ctx, client, "cloud.archive.export", params, progress, poll)
		if err != nil {
			return err
		}
		path := job.Path
		if destination != "" {
			if info, err := os.Stat(destination); err == nil && info.IsDir() {
				destination = filepath.Join(destination, job.Name)
			}
			if err := moveFile(job.Path, destination); err != nil {
				return err
			}
			path = destination
		}
		fmt.Fprintln(output, path)
		return nil
	}
	return usage
}

type archiveStatus struct {
	State  string               `json:"state"`
	Bytes  int64                `json:"bytes"`
	Total  int64                `json:"total"`
	Path   string               `json:"path"`
	Name   string               `json:"name"`
	Result *cloudarchive.Result `json:"result"`
	Error  string               `json:"error"`
}

// runArchiveJob starts a job and follows it to the end. Interrupting the
// command cancels the job.
func runArchiveJob(ctx context.Context, client cloudCaller, method string, params any, progress io.Writer, poll time.Duration) (archiveStatus, error) {
	var job archiveStatus
	if err := client.Call(ctx, method, params, &job); err != nil {
		return job, err
	}
	for job.State == "running" {
		select {
		case <-ctx.Done():
			cancel, stop := context.WithTimeout(context.Background(), 5*time.Second)
			client.Call(cancel, "cloud.archive.cancel", nil, nil)
			stop()
			return job, ctx.Err()
		case <-time.After(poll):
		}
		if err := client.Call(ctx, "cloud.archive.status", nil, &job); err != nil {
			return job, err
		}
		if job.Total > 0 {
			fmt.Fprintf(progress, "\r%d / %d MB", job.Bytes>>20, job.Total>>20)
		} else {
			fmt.Fprintf(progress, "\r%d MB", job.Bytes>>20)
		}
	}
	fmt.Fprintln(progress)
	if job.State != "done" {
		if job.Error == "" {
			job.Error = job.State
		}
		return job, fmt.Errorf("%s", job.Error)
	}
	return job, nil
}

// moveFile renames, or copies across volumes.
func moveFile(source, destination string) error {
	if err := os.Rename(source, destination); err == nil {
		return nil
	}
	in, err := os.Open(source)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		os.Remove(destination)
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Remove(source)
}
