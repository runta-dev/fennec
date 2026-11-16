package relay

import "testing"

func TestDelivery(t *testing.T) { s:=Store{Delivered:map[string]int{}}; s.Deliver(Event{ID:"evt_1"}); if s.Delivered["evt_1"] != 1 { t.Fatal(s.Delivered) } }
func TestAuthorized(t *testing.T) { if !Authorize(APIKey{Workspace:"acme"}, "acme") { t.Fatal("valid key rejected") } }
func TestRetry(t *testing.T) { if RetryDelay(0)!=1 { t.Fatal("first retry should wait one second") } }
