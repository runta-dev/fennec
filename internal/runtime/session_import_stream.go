package runtime

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"time"
)

type SessionImportQuery struct {
	Agent        string `json:"agent"`
	Conversation string `json:"conversation"`
	Marker       string `json:"marker"`
}
type SessionImportReply struct {
	Present bool   `json:"present,omitempty"`
	Written int    `json:"written,omitempty"`
	Error   string `json:"error,omitempty"`
}

// DialSessionImport exposes only conversation transfer on a credited resident
// socket stream. An explicit compressed length supplies EOF without closing
// the reply direction or the shared SSH channel.
func DialSessionImport(ctx context.Context, home string) net.Conn {
	client, server := net.Pipe()
	go func() {
		defer server.Close()
		server.SetDeadline(time.Now().Add(3 * time.Minute))
		stop := context.AfterFunc(ctx, func() { server.Close() })
		defer stop()
		err := serveSessionImport(home, server)
		if err != nil {
			json.NewEncoder(server).Encode(SessionImportReply{Error: "conversation import failed"})
		}
	}()
	return client
}

func serveSessionImport(home string, conn io.ReadWriter) error {
	reader := bufio.NewReaderSize(conn, 4096)
	line, err := reader.ReadSlice('\n')
	if err != nil {
		return err
	}
	var query SessionImportQuery
	if json.Unmarshal(line, &query) != nil || !validConversation(query.Agent, query.Conversation) || !markerPattern.MatchString(query.Marker) {
		return fmt.Errorf("invalid conversation query")
	}
	present := HasCloudSession(home, query.Agent, query.Conversation, query.Marker)
	if err := json.NewEncoder(conn).Encode(SessionImportReply{Present: present}); err != nil {
		return err
	}
	if present {
		return nil
	}
	var size uint64
	if err := binary.Read(reader, binary.BigEndian, &size); err != nil {
		return err
	}
	if size == 0 || size > sessionImportLimit+(8<<20) {
		return fmt.Errorf("invalid conversation payload size")
	}
	written, err := ReceiveSessionImport(home, io.LimitReader(reader, int64(size)))
	if err != nil {
		return err
	}
	return json.NewEncoder(conn).Encode(SessionImportReply{Written: written})
}
