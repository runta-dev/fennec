package cli

import (
	"bytes"
	"encoding/xml"
	"io"
	"strings"
	"testing"
)

func TestRolloverStartsExactInstalledBinaryWithoutAShell(t *testing.T) {
	binary := "/Applications/Owner's & Team <Fennec>.app/Contents/MacOS/fen"
	data := rolloverPlist(binary)
	decoder := xml.NewDecoder(bytes.NewReader(data))
	var values []string
	for {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		if start, ok := token.(xml.StartElement); ok && start.Name.Local == "string" {
			var value string
			if err := decoder.DecodeElement(&value, &start); err != nil {
				t.Fatal(err)
			}
			values = append(values, value)
		}
	}
	if len(values) != 6 || values[0] != rolloverLabel || values[1] != binary || values[2] != "start" || values[3] != "Interactive" {
		t.Fatalf("incorrect launch arguments: %#v", values)
	}
	for _, forbidden := range []string{"KeepAlive", "sleep", "/bin/sh", "control-daemon"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("starter must not own daemon lifetime: %s", forbidden)
		}
	}
}
