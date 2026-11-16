package main

import (
	"context"
	"errors"
	"io"

	"github.com/pkg/sftp"
)

type sftpStdio struct {
	io.ReadCloser
	output io.WriteCloser
}

func (s sftpStdio) Write(p []byte) (int, error) { return s.output.Write(p) }
func (s sftpStdio) Close() error {
	return errors.Join(s.ReadCloser.Close(), s.output.Close())
}

// This endpoint runs on the cloud as the already authenticated command owner.
// Transport authentication and pacing belong to the resident command stream.
// It is not the Mac SSH approval/SFTP service.
func serveSFTP(ctx context.Context, home string, input io.ReadCloser, output io.WriteCloser) error {
	server, err := sftp.NewServer(sftpStdio{input, output}, sftp.WithServerWorkingDirectory(home))
	if err != nil {
		return err
	}
	defer server.Close()
	stop := context.AfterFunc(ctx, func() { server.Close() })
	defer stop()
	err = server.Serve()
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}
