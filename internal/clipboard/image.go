// Package clipboard reads an image only in response to a user's paste gesture.
package clipboard

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image/png"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"
)

const MaxImageBytes = 20 << 20

// ValidatePNG bounds both the encoded size and decoded allocation.
func ValidatePNG(data []byte) error {
	if len(data) == 0 || len(data) > MaxImageBytes {
		return fmt.Errorf("image must be at most 20 MB")
	}
	config, err := png.DecodeConfig(bytes.NewReader(data))
	if err != nil || config.Width < 1 || config.Height < 1 || config.Width > 16384 || config.Height > 16384 || int64(config.Width)*int64(config.Height) > 32<<20 {
		return fmt.Errorf("invalid or oversized PNG image")
	}
	if _, err := png.Decode(bytes.NewReader(data)); err != nil {
		return fmt.Errorf("invalid PNG image")
	}
	return nil
}

// ReadImage returns nil when the pasteboard has no image. The signed native
// helper only adapts AppKit's pasteboard and TIFF representation to PNG; it
// never modifies the clipboard or writes an image to the Mac's filesystem.
func ReadImage(ctx context.Context) ([]byte, error) {
	if runtime.GOOS != "darwin" {
		return nil, nil
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	executable, err = filepath.EvalSymlinks(executable)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, filepath.Join(filepath.Dir(executable), "fennec-clipboard"))
	pipe, err := command.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := command.Start(); err != nil {
		return nil, fmt.Errorf("image paste helper unavailable")
	}
	data, readErr := io.ReadAll(io.LimitReader(pipe, MaxImageBytes+1))
	if len(data) > MaxImageBytes || readErr != nil {
		cancel()
	}
	err = command.Wait()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 2 {
		return nil, nil
	}
	if err != nil || readErr != nil {
		return nil, fmt.Errorf("could not read clipboard image")
	}
	if err := ValidatePNG(data); err != nil {
		return nil, err
	}
	return data, nil
}
