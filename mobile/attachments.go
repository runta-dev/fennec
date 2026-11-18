package fennecmobile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"fennec/internal/clipboard"
	cloudruntime "fennec/internal/runtime"
	"fennec/internal/sessionid"
	"fennec/internal/terminalstream"
	"golang.org/x/crypto/ssh"
)

const maxAttachmentBytes = 64 << 20

// resident shares a single protocol channel with commands, transfers and pastes.
func (s *Sessions) resident(ctx context.Context, client *ssh.Client) (*terminalstream.Transport, error) {
	s.residentMu.Lock()
	defer s.residentMu.Unlock()
	if s.transport != nil {
		select {
		case <-s.transport.Done():
			s.transport.Close()
			s.transport = nil
		default:
		}
	}
	if s.transport != nil && s.residentClient != client {
		s.transport.Close()
		s.transport = nil
	}
	if s.transport == nil {
		transport, err := terminalstream.StartSSHContext(ctx, client, `exec "$HOME/.local/fennec/bin/fennec-terminal" serve`)
		if err != nil {
			return nil, err
		}
		if err = transport.Ready(ctx); err != nil {
			transport.Close()
			return nil, err
		}
		s.transport, s.residentClient = transport, client
	}
	return s.transport, nil
}

// SendImages uses the same native TUI image frames as the desktop client.
// Every paste is acknowledged before the next image, text, or Return is sent.
// Never retry after an uncertain delivery: the native composer may hold it.
func (s *Sessions) SendImages(address, sessionID, text, imagePathsJSON string) (string, error) {
	id := sessionID
	if !addressPattern.MatchString(address) || !sessionid.Valid(id) || len(text) > 16384 || strings.ContainsAny(text, "\x00\x1b") {
		return "", errors.New("invalid message")
	}
	var paths []string
	if json.Unmarshal([]byte(imagePathsJSON), &paths) != nil || len(paths) == 0 || len(paths) > 8 {
		return "", errors.New("select up to eight images")
	}
	images := make([][]byte, len(paths))
	total := 0
	for i, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			return "", err
		}
		data, err := io.ReadAll(io.LimitReader(f, clipboard.MaxImageBytes+1))
		f.Close()
		if err != nil {
			return "", err
		}
		if err = clipboard.ValidatePNG(data); err != nil {
			return "", err
		}
		total += len(data)
		if total > maxAttachmentBytes {
			return "", errors.New("images must total at most 64 MB")
		}
		images[i] = data
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	client, _, err := s.connect(ctx)
	if err != nil {
		return "", err
	}
	transport, err := s.resident(ctx, client)
	if err != nil {
		return "", err
	}
	streamID, err := sessionid.Random()
	if err != nil {
		return "", err
	}
	stream, err := transport.Launch(terminalstream.Frame{Type: "attach", ID: streamID, Terminal: &cloudruntime.LocalTerminal{ID: id, Address: address}, Rows: 40, Columns: 100, Features: []string{terminalstream.CapabilityEchoAck}})
	if err != nil {
		return "", err
	}
	defer stream.Close()
	if err := sendNativeImages(ctx, stream, images, text); err != nil {
		return "", err
	}
	return `{}`, nil
}

type imageStream interface {
	Send(terminalstream.Frame) error
	Next(context.Context) (terminalstream.Frame, error)
}

func sendNativeImages(ctx context.Context, stream imageStream, images [][]byte, text string) error {
	window := terminalstream.NewCreditWindow()
	defer window.Close()
	life, cancel := context.WithCancel(ctx)
	defer cancel()
	stop := context.AfterFunc(life, window.Close)
	defer stop()
	events := make(chan terminalstream.Frame, 16)
	failure := make(chan error, 1)
	go func() {
		for {
			frame, err := stream.Next(life)
			if err == nil {
				switch frame.Type {
				case "data":
					err = stream.Send(terminalstream.Frame{Type: "credit", Bytes: len(frame.Data)})
				case "input_credit":
					if !window.Add(frame.Bytes) {
						err = errors.New("invalid input credit")
					}
				case "error", "paste_error", "exit":
					err = fmt.Errorf("image delivery not confirmed: %s", frame.Message)
				case "receipt", "echo_ack":
					select {
					case events <- frame:
					case <-life.Done():
						return
					}
				}
			}
			if err != nil {
				failure <- err
				cancel()
				return
			}
		}
	}()
	wait := func(kind string, seq uint64) error {
		for {
			select {
			case frame := <-events:
				if frame.Type == kind && frame.Seq >= seq {
					if kind == "receipt" {
						found := false
						for _, capability := range frame.Capabilities {
							if capability == terminalstream.CapabilityEchoAck {
								found = true
							}
						}
						if !found {
							return errors.New("update the cloud client to send images")
						}
					}
					return nil
				}
			case err := <-failure:
				return err
			case <-life.Done():
				return life.Err()
			}
		}
	}
	if err := wait("receipt", 0); err != nil {
		return err
	}
	seq := uint64(0)
	send := func(kind string, data []byte) error {
		seq++
		size := 0
		if kind == "image" {
			size = len(data)
		}
		for len(data) > 0 {
			n := window.Take(min(len(data), terminalstream.Chunk))
			if n == 0 {
				return errors.New("delivery interrupted; check the terminal before sending again")
			}
			if err := stream.Send(terminalstream.Frame{Type: kind, Data: data[:n], Bytes: size, Seq: seq}); err != nil {
				return err
			}
			data, size = data[n:], 0
		}
		return wait("echo_ack", seq)
	}
	for _, image := range images {
		if err := send("image", image); err != nil {
			return err
		}
	}
	if text != "" {
		if err := send("input", []byte("\x1b[200~"+text+"\x1b[201~")); err != nil {
			return err
		}
	}
	return send("input", []byte("\r"))
}
