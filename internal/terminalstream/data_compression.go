package terminalstream

import (
	"bytes"
	"compress/gzip"
	"fmt"
	"io"
	"sync"
)

var terminalCompressors = sync.Pool{New: func() any {
	w, _ := gzip.NewWriterLevel(io.Discard, gzip.BestSpeed)
	return w
}}

// Compress each bounded frame independently. There is no dictionary shared
// between sessions, and the flow-control budget always counts original bytes.
func compressTerminalData(frame Frame) Frame {
	if frame.Type != "data" || len(frame.Data) < 512 || len(frame.Data) > Chunk {
		return frame
	}
	var encoded bytes.Buffer
	w := terminalCompressors.Get().(*gzip.Writer)
	w.Reset(&encoded)
	_, err := w.Write(frame.Data)
	closeErr := w.Close()
	w.Reset(io.Discard)
	terminalCompressors.Put(w)
	if err != nil || closeErr != nil || encoded.Len()+32 >= len(frame.Data) {
		return frame
	}
	frame.Data, frame.DataGZIP = nil, encoded.Bytes()
	return frame
}

func expandTerminalData(frame *Frame) error {
	if len(frame.DataGZIP) == 0 {
		return nil
	}
	if frame.Type != "data" || len(frame.Data) != 0 || len(frame.DataGZIP) > Chunk {
		return fmt.Errorf("invalid compressed terminal frame")
	}
	r, err := gzip.NewReader(bytes.NewReader(frame.DataGZIP))
	if err != nil {
		return fmt.Errorf("invalid terminal compression")
	}
	defer r.Close()
	data, err := io.ReadAll(io.LimitReader(r, Chunk+1))
	if err != nil || len(data) == 0 || len(data) > Chunk {
		return fmt.Errorf("invalid terminal compression size or checksum")
	}
	frame.Data, frame.DataGZIP = data, nil
	return nil
}
