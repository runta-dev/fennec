package chatlog

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"fennec/internal/transcript"
)

// RefPrefix starts an image reference in place of a data: URL in an item's
// images (Reader.LazyImages).
const RefPrefix = "fennec-log-image:"

// ImageRef names image index of the item with this ID in the log at path:
// "fennec-log-image:<id>:<index>:<path>".
func ImageRef(path string, id int64, index int) string {
	return fmt.Sprintf("%s%d:%d:%s", RefPrefix, id, index, path)
}

// ParseImageRef reads an ImageRef back.
func ParseImageRef(ref string) (path string, id int64, index int, err error) {
	rest, ok := strings.CutPrefix(ref, RefPrefix)
	parts := strings.SplitN(rest, ":", 3)
	if !ok || len(parts) != 3 {
		return "", 0, 0, errors.New("invalid image reference")
	}
	id, err1 := strconv.ParseInt(parts[0], 10, 64)
	index, err2 := strconv.Atoi(parts[1])
	path = parts[2]
	if err1 != nil || err2 != nil || id < 0 || index < 0 || !strings.HasPrefix(path, "/") || !strings.HasSuffix(path, ".jsonl") || strings.ContainsAny(path, "\n\x00") {
		return "", 0, 0, errors.New("invalid image reference")
	}
	return path, id, index, nil
}

// Image returns the data: URL of image index of the item with this ID
// (transcript.Item.ID: the line's byte offset × 16 + the item's place in it).
func Image(path string, id int64, index int) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	offset := id / 16
	if _, err := file.Seek(offset, io.SeekStart); err != nil {
		return "", err
	}
	line, err := bufio.NewReaderSize(io.LimitReader(file, MaxLine), 1<<20).ReadBytes('\n')
	if err != nil && !(errors.Is(err, io.EOF) && len(line) > 0) {
		return "", errors.New("image not found")
	}
	for _, item := range transcript.Parse(line, offset) {
		if item.ID == id && index < len(item.Images) {
			return item.Images[index], nil
		}
	}
	return "", errors.New("image not found")
}
