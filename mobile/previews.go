package fennecmobile

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"fennec/internal/preview"
	"fennec/internal/sessionid"
	"fennec/internal/terminalstream"
)

// PreviewParts is a shared parser for live/cached agent messages and uploaded
// attachment references. Only explicit Markdown preview references are rendered.
func PreviewParts(text string) string {
	data, _ := json.Marshal(preview.Parse(text))
	return string(data)
}

func (s *Sessions) previewClient(ctx context.Context) (*http.Client, func(), error) {
	client, _, err := s.connect(ctx)
	if err != nil {
		return nil, nil, err
	}
	resident, err := s.resident(ctx, client)
	if err != nil {
		return nil, nil, err
	}
	if !resident.Supports(terminalstream.CapabilityPreview) {
		return nil, nil, errors.New("update Fennec on the cloud computer to use attachments")
	}
	transport := &http.Transport{DisableKeepAlives: true, DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return resident.DialSocket(ctx, terminalstream.SocketPreview, "")
	}}
	return &http.Client{Transport: transport, Timeout: 3 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("preview redirect refused") }}, transport.CloseIdleConnections, nil
}
func previewEndpoint(id, ref string) (string, error) {
	if !sessionid.Valid(id) || !preview.RefPattern.MatchString(ref) {
		return "", errors.New("invalid preview reference")
	}
	return "http://preview.fennec/v1/sessions/" + id + "/previews/" + ref, nil
}
func checkPreviewResponse(response *http.Response) error {
	if response.StatusCode == http.StatusOK {
		return nil
	}
	switch response.StatusCode {
	case http.StatusNotFound:
		return errors.New("this preview is no longer available")
	case http.StatusRequestEntityTooLarge:
		return errors.New("attachments must be at most 64 MB")
	}
	return fmt.Errorf("preview request failed (%d)", response.StatusCode)
}
func (s *Sessions) PreviewMetadata(id, ref string) (out0 string, err error) {
	defer func() { err = classify(err) }()
	endpoint, err := previewEndpoint(id, ref)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client, close, err := s.previewClient(ctx)
	if err != nil {
		return "", err
	}
	defer close()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if err = checkPreviewResponse(response); err != nil {
		return "", err
	}
	var metadata preview.Metadata
	if err = json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&metadata); err != nil {
		return "", err
	}
	if metadata.Ref != ref {
		return "", errors.New("preview reference mismatch")
	}
	data, err := json.Marshal(metadata)
	return string(data), err
}

func (s *Sessions) DownloadPreview(id, ref, destination string) (err error) {
	defer func() { err = classify(err) }()
	endpoint, err := previewEndpoint(id, ref)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	client, close, err := s.previewClient(ctx)
	if err != nil {
		return err
	}
	defer close()
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"/content", nil)
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if err = checkPreviewResponse(response); err != nil {
		return err
	}
	return savePreview(response, destination)
}
func savePreview(response *http.Response, destination string) error {
	if response.ContentLength < 0 || response.ContentLength > preview.MaxBytes {
		return errors.New("invalid preview size")
	}
	file, err := os.CreateTemp(filepath.Dir(destination), ".preview-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	digest := sha256.New()
	n, err := io.Copy(io.MultiWriter(file, digest), io.LimitReader(response.Body, preview.MaxBytes+1))
	if err != nil {
		return err
	}
	if n != response.ContentLength || n > preview.MaxBytes {
		return errors.New("incomplete preview")
	}
	if response.Header.Get("ETag") != `"`+hex.EncodeToString(digest.Sum(nil))+`"` {
		return errors.New("preview content verification failed")
	}
	if err = file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), destination)
}

// UploadFile returns the canonical workspace path and registered preview metadata.
// uploadID comes from the draft's UUID and is retained across manual retries.
func (s *Sessions) UploadFile(address, sessionID, uploadID, localPath, name string) (out0 string, err error) {
	defer func() { err = classify(err) }()
	id := sessionID
	if !addressPattern.MatchString(address) || !sessionid.Valid(id) {
		return "", errors.New("invalid session")
	}
	file, err := os.Open(localPath)
	if err != nil {
		return "", err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() || info.Size() > preview.MaxBytes {
		return "", errors.New("files must be at most 64 MB")
	}
	if _, err = s.Read(address, id); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	client, close, err := s.previewClient(ctx)
	if err != nil {
		return "", err
	}
	defer close()
	endpoint := "http://preview.fennec/v1/sessions/" + id + "/uploads/" + url.PathEscape(uploadID) + "?name=" + url.QueryEscape(name)
	request, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, io.LimitReader(file, info.Size()))
	if err != nil {
		return "", err
	}
	request.ContentLength = info.Size()
	request.Header.Set("Content-Type", "application/octet-stream")
	response, err := client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if err = checkPreviewResponse(response); err != nil {
		return "", err
	}
	var result preview.Upload
	if err = json.NewDecoder(io.LimitReader(response.Body, 8192)).Decode(&result); err != nil {
		return "", err
	}
	data, err := json.Marshal(result)
	return string(data), err
}
