package browserset

import (
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	browsersite "fennec/cua/bench/browser"
)

// testServer serves the embedded site with a log in a temporary folder.
func testServer(t *testing.T, dir string) *httptest.Server {
	t.Helper()
	site, err := fs.Sub(browsersite.FS, "site")
	if err != nil {
		t.Fatal(err)
	}
	l, err := OpenLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	h, err := NewServer(site, l)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, u string) (*http.Response, string) {
	t.Helper()
	resp, err := http.Get(u)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, string(b)
}

func TestServerLogsPagesSubmissionsDownloadsAndEvents(t *testing.T) {
	dir := t.TempDir()
	srv := testServer(t, dir)

	resp, body := get(t, srv.URL+"/fernhollow/checkout")
	if resp.StatusCode != 200 || !strings.Contains(body, "Fernhollow Seed Co.") {
		t.Fatalf("checkout page: %d %q", resp.StatusCode, body[:min(80, len(body))])
	}
	if resp, _ := get(t, srv.URL+"/static/site.css"); resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/css") {
		t.Fatalf("css: %d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
	if resp, _ := get(t, srv.URL+"/wrenfield/"); resp.StatusCode != 200 {
		t.Fatalf("directory index: %d", resp.StatusCode)
	}
	// Templates are never served as files, and unknown paths are 404.
	for _, p := range []string{"/larkspur/shipments.tmpl", "/nowhere", "/marrowby/files/other.csv"} {
		if resp, _ := get(t, srv.URL+p); resp.StatusCode != 404 {
			t.Errorf("%s: %d, want 404", p, resp.StatusCode)
		}
	}
	form := url.Values{"topics": {"local", "weather"}, "email": {"a@example.test"}}
	presp, err := http.PostForm(srv.URL+"/moorfield/subscribe", form)
	if err != nil {
		t.Fatal(err)
	}
	presp.Body.Close()
	resp, body = get(t, srv.URL+"/marrowby/files/circulation-2026-q3-final.csv")
	want, _ := Report("circulation-2026-q3-final.csv")
	if body != string(want) || !strings.Contains(resp.Header.Get("Content-Disposition"), `filename="circulation-2026-q3-final.csv"`) {
		t.Fatalf("download: %q %q", resp.Header.Get("Content-Disposition"), body)
	}
	if resp, _ := get(t, srv.URL+"/event/lure"); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("event: %d", resp.StatusCode)
	}
	if resp, _ := get(t, srv.URL+"/healthz"); resp.StatusCode != 200 {
		t.Fatalf("healthz: %d", resp.StatusCode)
	}

	log, err := ReadLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for i, e := range log {
		if e.Seq != i+1 {
			t.Errorf("entry %d has seq %d", i, e.Seq)
		}
		got = append(got, e.Kind+" "+e.Path)
	}
	wantLog := "page /fernhollow/checkout|page /wrenfield/|submit /moorfield/subscribe|download /marrowby/files/circulation-2026-q3-final.csv|event /event/lure"
	if strings.Join(got, "|") != wantLog {
		t.Fatalf("log:\n%s\nwant\n%s", strings.Join(got, "|"), wantLog)
	}
	if f := log[2].Fields; strings.Join(f["topics"], ",") != "local,weather" || f["email"][0] != "a@example.test" {
		t.Fatalf("fields: %v", f)
	}

	// A reopened log continues the sequence.
	l, err := OpenLog(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := l.Add(Entry{Kind: KindEvent, Path: "/event/again"}); err != nil {
		t.Fatal(err)
	}
	l.Close()
	log, _ = ReadLog(dir)
	if last := log[len(log)-1]; last.Seq != 6 || last.Path != "/event/again" {
		t.Fatalf("reopened log: %+v", last)
	}
}

func TestShipmentsPages(t *testing.T) {
	srv := testServer(t, t.TempDir())
	_, page9 := get(t, srv.URL+"/larkspur/shipments?page=9")
	if !strings.Contains(page9, "<td>LF-40731</td>") || !strings.Contains(page9, "<td>37.4</td>") {
		t.Fatal("page 9 lacks the target shipment")
	}
	_, page1 := get(t, srv.URL+"/larkspur/shipments")
	if strings.Contains(page1, "LF-40731") || !strings.Contains(page1, "page 1 of 12") {
		t.Fatal("page 1 is wrong")
	}
	_, last := get(t, srv.URL+"/larkspur/shipments?page=99")
	if !strings.Contains(last, "page 12 of 12") {
		t.Fatal("an out-of-range page is not clamped")
	}
	_, inv := get(t, srv.URL+"/copperleaf/invoices")
	if strings.Count(inv, `class="inv"`) != 18 || !strings.Contains(inv, "$1,045.00") {
		t.Fatal("invoices page is wrong")
	}
}

func TestDataIsDeterministic(t *testing.T) {
	a, b := Shipments(), Shipments()
	if len(a) != 120 {
		t.Fatalf("%d shipments", len(a))
	}
	ids := map[string]bool{}
	targetPage := 0
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("row %d differs between calls", i)
		}
		if ids[a[i].ID] {
			t.Fatalf("duplicate id %s", a[i].ID)
		}
		ids[a[i].ID] = true
		if a[i].ID == TargetShipment {
			targetPage = i/ShipmentsPerPage + 1
		}
		if i > 0 && a[i].Date > a[i-1].Date {
			t.Fatalf("not newest first at row %d", i)
		}
	}
	if targetPage != 9 {
		t.Fatalf("target on page %d", targetPage)
	}
	lookalikes := 0
	for id := range ids {
		if sameDigits(id, TargetShipment) {
			lookalikes++
		}
	}
	if lookalikes != 4 { // the target and its three planted decoys
		t.Fatalf("%d ids with the target's digits", lookalikes)
	}
	sum := 0.0
	for _, inv := range Invoices() {
		if inv.Status == "Overdue" {
			f, ok := parseNumber(inv.Amount)
			if !ok {
				t.Fatal(inv.Amount)
			}
			sum += f
		}
	}
	if want, _ := parseNumber(OverdueTotal); sum < want-0.001 || sum > want+0.001 {
		t.Fatalf("overdue sum %.2f, want %s", sum, OverdueTotal)
	}
	r1, _ := Report("circulation-2026-q3-final.csv")
	r2, _ := Report("circulation-2026-q3-draft.csv")
	if string(r1) == string(r2) {
		t.Fatal("the final and draft reports are identical")
	}
}
