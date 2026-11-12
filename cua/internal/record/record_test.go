package record

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"fennec/cua/internal/view"
)

func saveHit() *Hit {
	return &Hit{Role: "button", Name: "Save", PID: 7, Path: []PathNode{{Role: "window", Name: "Untitled", Key: "7/1"}}}
}

func TestClickKeepsElementWindowAndCrop(t *testing.T) {
	d := newDesk(saveHit())
	r := record(t, d, nil, click(100, 80))
	e := r.Events[0]
	if e.Kind != KindClick || e.Element == nil || e.Element.Name != "Save" || e.App != "TextEdit" || e.Root {
		t.Fatalf("event = %+v", e)
	}
	if e.Window == nil || e.Window.Title != "Untitled" || *e.Window.Bounds != [4]int{10, 20, 300, 200} {
		t.Fatalf("window = %+v", e.Window)
	}
	if e.Button != "left" || e.Count != 1 || *e.X != 100 || *e.Y != 80 {
		t.Errorf("point and button = %v %v %s %d", *e.X, *e.Y, e.Button, e.Count)
	}
	if len(e.Path) != 1 || e.Path[0].Name != "Untitled" {
		t.Errorf("path = %+v", e.Path)
	}
	if w := pngWidth(t, e.Crop); w != 190 {
		t.Errorf("crop width = %d, want 190 (clipped at the window's left edge)", w)
	}
	if e.CropScale != 1 || *e.CropOffset != [2]float64{-5, 0} {
		t.Errorf("crop scale %v offset %v", e.CropScale, *e.CropOffset)
	}

	r = record(t, d, nil, click(30, 20))
	e = r.Events[0]
	if w := pngWidth(t, e.Crop); w != 120 {
		t.Errorf("corner crop width = %d, want 120", w)
	}
	if *e.CropOffset != [2]float64{-40, -25} {
		t.Errorf("corner offset = %v", *e.CropOffset)
	}
	for _, s := range d.shots {
		if s.Key != "7/1" || s.PID != 7 {
			t.Errorf("shot of %+v", s)
		}
	}

	// At two pixels per point the crop covers 400 by 200 desktop units.
	big := newDesk(saveHit())
	big.windows[0].Bounds = &[4]int{0, 0, 1000, 600}
	q := newQueue()
	q.push(click(500, 300))
	q.close()
	s := &session{id: "r", perPoint: 2, settle: 20 * time.Millisecond, self: 4242, desk: big, inputs: q}
	e = s.work(context.Background()).Events[0]
	if w := pngWidth(t, e.Crop); w != 400 || *e.CropOffset != [2]float64{0, 0} {
		t.Errorf("retina crop width %d offset %v", w, *e.CropOffset)
	}
}

func TestTapReadElementWins(t *testing.T) {
	d := newDesk(&Hit{Role: "generic", Name: "Sidebar", PID: 3})
	menu := click(160, 48)
	menu.Hit = &Hit{Role: "menuitem", Name: "New", PID: 7, Path: []PathNode{{Role: "menu", Name: "File"}, {Role: "window", Name: "Untitled"}}}
	r := record(t, d, nil, menu)
	e := r.Events[0]
	if e.Element.Name != "New" || e.App != "TextEdit" || e.Window == nil || e.Window.Title != "Untitled" {
		t.Fatalf("event = %+v", e)
	}
	if e.Web != nil {
		t.Errorf("web = %+v", e.Web)
	}
}

func TestClickOnRootIsMarked(t *testing.T) {
	d := newDesk(&Hit{Role: "document", PID: 7, Path: []PathNode{{Role: "window", Name: "Untitled", Key: "7/1"}}})
	if r := record(t, d, nil, click(100, 80)); !r.Events[0].Root {
		t.Errorf("a document click is not root: %+v", r.Events[0])
	}
	for _, role := range []string{"window", "frame", "application", "dialog"} {
		d.hit.Role = role
		if r := record(t, d, nil, click(100, 80)); !r.Events[0].Root {
			t.Errorf("%s click is not root", role)
		}
	}
	none := newDesk(nil)
	if r := record(t, none, nil, click(100, 80)); r.Events[0].Kind != KindClick || !r.Events[0].Root {
		t.Errorf("a click on no element = %+v", r.Events[0])
	}
}

func TestClickInNewWindowRelists(t *testing.T) {
	d := newDesk(&Hit{Role: "button", Name: "Save", PID: 7, Path: []PathNode{{Role: "window", Name: "report.rtf", Key: "7/1"}}})
	feed, end := running(d, nil, time.Hour)
	time.Sleep(20 * time.Millisecond)
	d.setFront("report.rtf")
	feed(click(50, 50))
	r := end()
	if e := r.Events[0]; e.Window == nil || e.Window.Title != "report.rtf" || e.Crop == "" {
		t.Fatalf("event = %+v", e)
	}
}

func TestHoldsWebTreeUntilStop(t *testing.T) {
	d := newDesk(nil)
	record(t, d, nil, click(5, 5))
	if !reflect.DeepEqual(d.holds, []int{7}) || d.released != 1 {
		t.Errorf("holds %v released %d; want [7] and one release", d.holds, d.released)
	}
	other := newDesk(nil)
	record(t, other, []string{"Notes"}, click(5, 5))
	if len(other.holds) != 0 {
		t.Errorf("held the tree of an app the recording leaves out: %v", other.holds)
	}
}

func TestWebClickKeepsURLAndDOMID(t *testing.T) {
	d := newDesk(nil)
	pay := func(web *Web) Input {
		in := click(120, 80)
		in.Hit = &Hit{Role: "button", Name: "Pay", PID: 7, Path: []PathNode{{Role: "window", Name: "Untitled"}}, Web: web}
		return in
	}
	r := record(t, d, nil, pay(&Web{URL: "https://shop.test/cart", ID: "pay"}), pay(&Web{URL: "https://shop.test/cart"}))
	if got := r.Events[0].Web; got == nil || *got != (Web{URL: "https://shop.test/cart", ID: "pay"}) {
		t.Errorf("first web = %+v", got)
	}
	if got := r.Events[1].Web; got == nil || *got != (Web{URL: "https://shop.test/cart"}) {
		t.Errorf("second web = %+v", got)
	}
	raw, _ := json.Marshal(r.Events[1])
	if strings.Contains(string(raw), `"id"`) {
		t.Errorf("an element without a DOM id wrote one: %s", raw)
	}
}

func TestFirstKeyKeepsFocus(t *testing.T) {
	d := newDesk(nil)
	field := &Field{Role: "textbox", Name: "Save As:", Frame: "dialog", Window: "Untitled", URL: "https://a.test/", ID: "name"}
	first := key("a", false)
	first.Focus, first.Read = field, true
	later := key("b", false)
	unread := key("c", false)
	unread.Read = true
	r := record(t, d, nil, first, later, unread)
	raw := make([]map[string]json.RawMessage, 3)
	for i, e := range r.Events {
		b, _ := json.Marshal(e)
		_ = json.Unmarshal(b, &raw[i])
	}
	if r.Events[0].Focus == nil || *r.Events[0].Focus != *field {
		t.Errorf("first key focus = %+v", r.Events[0].Focus)
	}
	if _, ok := raw[1]["focus"]; ok {
		t.Errorf("a later key of the run carries a focus: %v", raw[1])
	}
	if f, ok := raw[2]["focus"]; !ok || string(f) != "null" {
		t.Errorf("an unreadable focus is not an explicit null: %v", raw[2])
	}
	if r.Events[0].App != "TextEdit" {
		t.Errorf("a key with no process went to %q, want the front app", r.Events[0].App)
	}
}

func TestPasswordFieldMarked(t *testing.T) {
	d := newDesk(nil)
	field := click(20, 20)
	field.Hit = &Hit{Role: "textbox", PID: 7, Secret: true}
	r := record(t, d, nil, field, click(5, 5), Input{Kind: InputPassword, PID: 7, Focus: &Field{Role: "textbox"}})
	if !r.Events[0].Secret || r.Events[1].Secret {
		t.Errorf("secret marks: %v %v", r.Events[0].Secret, r.Events[1].Secret)
	}
	p := r.Events[2]
	if p.Kind != KindPassword || p.App != "TextEdit" || p.Focus == nil || !p.Focus.Secure || p.Focus.Role != "textbox" {
		t.Errorf("password event = %+v focus %+v", p, p.Focus)
	}
}

func TestPasswordFocusOwnerApp(t *testing.T) {
	d := newDesk(nil)
	focus := func(pid int) Input {
		return Input{Kind: InputPassword, PID: pid, Focus: &Field{Secure: true}}
	}
	r := record(t, d, nil, focus(9), focus(0), focus(7))
	got := [][2]string{}
	for _, e := range r.Events {
		got = append(got, [2]string{e.Kind, e.App})
	}
	want := [][2]string{{KindSkipped, ""}, {KindSkipped, ""}, {KindPassword, "TextEdit"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("events = %v, want %v", got, want)
	}
	if r := record(t, d, []string{"Notes"}, focus(7)); r.Events[0].Kind != KindSkipped {
		t.Errorf("a password focus outside the recorded apps = %+v", r.Events[0])
	}
}

func TestSecureKeyHasNoText(t *testing.T) {
	d := newDesk(nil)
	secret := key("", true)
	secret.Text = "x" // what a tap must never pass on; the recorder drops it anyway
	r := record(t, d, nil, key("a", false), secret)
	if r.Events[0].Text != "a" || !r.Events[1].Secret {
		t.Fatalf("events = %+v", r.Events)
	}
	raw, _ := json.Marshal(r.Events[1])
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	for _, k := range []string{"text", "code", "focus"} {
		if _, ok := m[k]; ok {
			t.Errorf("a secret key keeps %s: %s", k, raw)
		}
	}
}

func TestBlockedAppsCaseInsensitive(t *testing.T) {
	for _, app := range []string{"fennec-cua", "SYSTEM SETTINGS", "keychain access", "1password", "passwords", "bitwarden", "LASTPASS", "dashlane", "keepassxc", "1Password 7"} {
		if !IsBlocked(app, "") || keeps(nil, app, 0, nil) {
			t.Errorf("%q is not blocked", app)
		}
	}
	if IsBlocked("TextEdit", "TextEdit") || !keeps(nil, "TextEdit", 0, nil) {
		t.Error("TextEdit is blocked")
	}
	if !keeps(nil, "textedit", 0, []string{"TextEdit"}) || keeps(nil, "Notes", 0, []string{"TextEdit"}) {
		t.Error("the apps a recording names do not match without case")
	}
}

func TestBlockedInputSkippedBare(t *testing.T) {
	d := newDesk(&Hit{Role: "textbox", Name: "Master password", PID: 9, Path: []PathNode{{Role: "window", Name: "Vault", Key: "9/1"}}})
	r := record(t, d, nil, click(5, 5))
	raw, _ := json.Marshal(r.Events[0])
	var m map[string]any
	_ = json.Unmarshal(raw, &m)
	if len(m) != 2 || m["kind"] != KindSkipped {
		t.Errorf("a blocked click = %s, want only t and kind skipped", raw)
	}
	for _, s := range d.shots {
		if s.Title == "Vault" {
			t.Error("the vault was captured")
		}
	}

	only := newDesk(saveHit())
	r = record(t, only, []string{"Notes"}, click(5, 5), key("x", false))
	if !reflect.DeepEqual(kinds(r), []string{KindSkipped, KindSkipped}) {
		t.Errorf("input outside the recorded apps = %v", kinds(r))
	}
	if len(only.shots) != 0 {
		t.Errorf("captured %d windows of an app the recording leaves out", len(only.shots))
	}

	vault := key("p", false)
	vault.PID = 9
	r = record(t, only, nil, key("a", false), vault)
	if !reflect.DeepEqual(kinds(r), []string{KindKey, KindSkipped}) {
		t.Errorf("a key into the vault = %v", kinds(r))
	}
	// A process that cannot be named is no app to give the input to.
	unnamed := newDesk(&Hit{Role: "button", Name: "Go", PID: 77})
	unnamed.windows = append(unnamed.windows, view.RawWindow{Key: "77/1", Title: "x", PID: 77, Role: "window"})
	if r := record(t, unnamed, nil, click(5, 5)); r.Events[0].Kind != KindSkipped || r.Events[0].App != "" {
		t.Errorf("a click in an unnamed app = %+v", r.Events[0])
	}
}

func TestWindowlessProcessSkipped(t *testing.T) {
	d := newDesk(&Hit{Role: "textbox", Name: "Search", PID: 42})
	panel := key("p", false)
	panel.PID = 42
	r := record(t, d, nil, click(5, 5), panel, key("a", false))
	got := [][2]string{}
	for _, e := range r.Events {
		got = append(got, [2]string{e.Kind, e.App})
	}
	want := [][2]string{{KindSkipped, ""}, {KindSkipped, ""}, {KindKey, "TextEdit"}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("events = %v, want %v", got, want)
	}
}

// fakeTap is a tap a test drives by hand.
type fakeTap struct {
	mu      sync.Mutex
	sink    func(Input)
	stopped bool
}

func (f *fakeTap) listen(sink func(Input)) (func(), error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sink = sink
	return func() { f.mu.Lock(); f.stopped = true; f.mu.Unlock() }, nil
}

func (f *fakeTap) send(in Input) {
	f.mu.Lock()
	sink := f.sink
	f.mu.Unlock()
	if in.At.IsZero() {
		in.At = time.Now()
	}
	sink(in)
}

func newRecorder(t *testing.T, d Desk, tap *fakeTap) *Recorder {
	return &Recorder{Dir: filepath.Join(t.TempDir(), "recordings"), Desk: d, Listen: tap.listen, Settle: 20 * time.Millisecond, Self: 4242}
}

func TestStopWaitsForSave(t *testing.T) {
	d := newDesk(nil)
	tap := &fakeTap{}
	r := newRecorder(t, d, tap)
	if _, err := r.Stop(context.Background()); err != ErrNone {
		t.Fatalf("stop with nothing recorded = %v", err)
	}
	if _, err := r.Start(nil); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Start(nil); err != ErrRunning {
		t.Fatalf("a second start = %v", err)
	}
	tap.send(key("a", false))
	time.Sleep(30 * time.Millisecond)
	gate := make(chan struct{})
	d.mu.Lock()
	d.gate = gate
	d.mu.Unlock()
	got := make(chan *Recording, 1)
	go func() {
		rec, err := r.Stop(context.Background())
		if err != nil {
			t.Error(err)
		}
		got <- rec
	}()
	select {
	case <-got:
		t.Fatal("stop answered before the recording was saved")
	case <-time.After(100 * time.Millisecond):
	}
	close(gate)
	var rec *Recording
	select {
	case rec = <-got:
	case <-time.After(2 * time.Second):
		t.Fatal("stop outlived the save")
	}
	saved, err := Load(rec.Path)
	if err != nil {
		t.Fatalf("stop answered before the file was written: %v", err)
	}
	a, _ := json.Marshal(rec)
	b, _ := json.Marshal(saved)
	if string(a) != string(b) {
		t.Errorf("answer and file differ:\n%s\n%s", a, b)
	}
	if !tap.stopped {
		t.Error("the tap still runs")
	}
	// The last recording answers again after it ended.
	again, err := r.Stop(context.Background())
	if err != nil || again.ID != rec.ID {
		t.Errorf("second stop = %v, %v", again, err)
	}
}

func TestKillHotkeyNotRecorded(t *testing.T) {
	d := newDesk(nil)
	press := func(code int) Input {
		return Input{Kind: InputKey, Code: code, Mods: []string{"ctrl", "alt", "cmd"}}
	}
	before := record(t, d, nil, press(1), press(53), Input{Kind: InputHotkey, Code: 53})
	after := record(t, d, nil, press(1), Input{Kind: InputHotkey, Code: 53}, press(53))
	for _, r := range []*Recording{before, after} {
		var codes []int
		for _, e := range r.Events {
			if e.Kind == KindKey {
				codes = append(codes, *e.Code)
			}
		}
		if !reflect.DeepEqual(codes, []int{1}) {
			t.Errorf("key codes = %v, want [1]", codes)
		}
	}
}

func TestAfterWindowKeptAtStop(t *testing.T) {
	d := newDesk(nil)
	settle := 30 * time.Millisecond
	feed, end := running(d, nil, settle)
	feed(key("s", false))
	time.Sleep(settle + 50*time.Millisecond)
	d.setFront("Paid")
	r := end()
	if len(r.Events) != 2 {
		t.Fatalf("events = %v", kinds(r))
	}
	last := r.Events[1]
	if last.Kind != KindAfter || last.Window.Title != "Paid" || last.App != "TextEdit" || last.Role != "window" {
		t.Errorf("last event = %+v", last)
	}
}

func TestAfterWindowsAndNotesInOrder(t *testing.T) {
	d := newDesk(nil)
	settle := 30 * time.Millisecond
	feed, end := running(d, nil, settle)
	feed(Input{Kind: InputNote, Note: "the name is a parameter"})
	feed(key("s", false))
	d.setFront("report.rtf")
	time.Sleep(settle + 80*time.Millisecond)
	r := end()
	if !reflect.DeepEqual(kinds(r), []string{KindNote, KindKey, KindAfter}) {
		t.Fatalf("kinds = %v", kinds(r))
	}
	if r.Events[0].Text != "the name is a parameter" || r.Events[2].Window.Title != "report.rtf" {
		t.Errorf("events = %+v", r.Events)
	}
	if Settle != 700*time.Millisecond {
		t.Errorf("the default settle is %v, want 700ms", Settle)
	}
}

func TestRedactions(t *testing.T) {
	d := newDesk(nil)
	d.windows = append(d.windows, view.RawWindow{Key: "11/1", Title: "Keychain", Role: "window", PID: 11, App: "Schlüsselbund", Bounds: &[4]int{0, 0, 50, 50}})
	d.bundles = map[int]string{11: "Keychain Access"}
	tap := &fakeTap{}
	r := newRecorder(t, d, tap)
	if _, err := r.Start(nil); err != nil {
		t.Fatal(err)
	}
	pw := key("", true)
	pw.Text = "hunter2"
	keychain := click(10, 10)
	keychain.Hit = &Hit{Role: "textbox", Name: "hunter3", PID: 11, Path: []PathNode{{Role: "window", Name: "Keychain", Key: "11/1"}}}
	menu := click(10, 10)
	menu.Hit = &Hit{Role: "menuitem", Name: "Stop fennec-cua hunter4", PID: 4242}
	for _, in := range []Input{pw, keychain, menu} {
		tap.send(in)
	}
	rec, err := r.Stop(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile(rec.Path)
	for _, s := range []string{"hunter2", "hunter3", "hunter4", "Keychain"} {
		if strings.Contains(string(body), s) {
			t.Errorf("the recording holds %q: %s", s, body)
		}
	}
	if !reflect.DeepEqual(kinds(rec), []string{KindKey, KindSkipped}) {
		t.Errorf("kinds = %v (a click on fennec-cua's own menu leaves no mark)", kinds(rec))
	}
	for _, s := range d.shots {
		if s.PID == 11 {
			t.Error("captured Keychain Access")
		}
	}
}

func TestFileMode(t *testing.T) {
	d := newDesk(nil)
	tap := &fakeTap{}
	r := newRecorder(t, d, tap)
	if err := os.MkdirAll(r.Dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Start(nil); err != nil {
		t.Fatal(err)
	}
	tap.send(key("a", false))
	rec, err := r.Stop(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if m := fileMode(t, r.Dir); m != 0o700 {
		t.Errorf("directory mode %o", m)
	}
	if m := fileMode(t, rec.Path); m != 0o600 {
		t.Errorf("file mode %o", m)
	}
}

func TestBlockedByBundleName(t *testing.T) {
	d := newDesk(&Hit{Role: "button", Name: "Wi-Fi", PID: 12, Path: []PathNode{{Role: "window", Name: "WLAN", Key: "12/1"}}})
	d.windows = append(d.windows, view.RawWindow{Key: "12/1", Title: "WLAN", Role: "window", PID: 12, App: "Systemeinstellungen", Bounds: &[4]int{0, 0, 50, 50}})
	d.bundles = map[int]string{12: "System Settings"}
	k := key("x", false)
	k.PID = 12
	r := record(t, d, nil, click(10, 10), k, Input{Kind: InputPassword, PID: 12})
	if !reflect.DeepEqual(kinds(r), []string{KindSkipped, KindSkipped, KindSkipped}) {
		t.Errorf("kinds = %v", kinds(r))
	}
	if len(d.shots) != 0 && d.shots[len(d.shots)-1].PID == 12 {
		t.Error("captured System Settings")
	}
	for _, pid := range d.holds {
		if pid == 12 {
			t.Error("held the page tree of a blocked app")
		}
	}
}

func TestOwnInputExcluded(t *testing.T) {
	clock := time.Unix(1000, 0)
	var mu sync.Mutex
	now := func() time.Time { mu.Lock(); defer mu.Unlock(); return clock }
	advance := func(d time.Duration) { mu.Lock(); clock = clock.Add(d); mu.Unlock() }
	acting := &Acting{now: now}
	d := newDesk(nil)
	tap := &fakeTap{}
	r := newRecorder(t, d, tap)
	r.Acting = acting
	if _, err := r.Start(nil); err != nil {
		t.Fatal(err)
	}
	at := func(text string) Input { k := key(text, false); k.At = now(); return k }
	tap.send(at("a")) // the user's
	end := acting.Begin()
	tap.send(at("b")) // fennec-cua's own, during its action
	advance(50 * time.Millisecond)
	end()
	advance(100 * time.Millisecond)
	tap.send(at("c")) // fennec-cua's own, heard just after the action ended
	advance(time.Second)
	tap.send(at("d")) // the user's again
	rec, err := r.Stop(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var typed string
	for _, e := range rec.Events {
		typed += e.Text
	}
	if typed != "ad" {
		t.Errorf("recorded %q, want only the user's keys \"ad\"", typed)
	}
}

func TestCropByPid(t *testing.T) {
	d := newDesk(nil)
	d.windows = append(d.windows, view.RawWindow{Key: "8/1", Title: "Untitled", Role: "window", PID: 8, App: "Notes", Bounds: &[4]int{400, 20, 300, 200}})
	in := click(500, 80)
	in.Hit = &Hit{Role: "button", Name: "Bold", PID: 8, Path: []PathNode{{Role: "window", Name: "Untitled"}}}
	r := record(t, d, nil, in)
	e := r.Events[0]
	if e.App != "Notes" || e.Crop == "" || e.Window == nil || (*e.Window.Bounds)[0] != 400 {
		t.Fatalf("event = %+v", e)
	}
	for _, s := range d.shots[1:] { // the first shot is the front window at the start
		if s.PID != 8 {
			t.Errorf("cropped the same-titled window of pid %d", s.PID)
		}
	}
	// A click deeper than the path reaches keeps its window and crop.
	deep := click(500, 80)
	path := make([]PathNode, 20)
	for i := range path {
		path[i] = PathNode{Role: "group"}
	}
	deep.Hit = &Hit{Role: "button", Name: "Bold", PID: 8, Path: path, Window: &PathNode{Role: "window", Name: "Untitled", Key: "8/1"}}
	r = record(t, d, nil, deep)
	if e := r.Events[0]; e.App != "Notes" || e.Crop == "" || e.Window == nil {
		t.Errorf("deep click = %+v", e)
	}
}

func TestStopSavesRecording(t *testing.T) {
	d := newDesk(nil)
	tap := &fakeTap{}
	r := newRecorder(t, d, tap)
	id, err := r.Start(nil)
	if err != nil {
		t.Fatal(err)
	}
	tap.send(key("a", false))
	if err := r.Finish(context.Background()); err != nil {
		t.Fatal(err)
	}
	rec, err := Load(filepath.Join(r.Dir, id+".json"))
	if err != nil {
		t.Fatalf("the recording in progress was not saved: %v", err)
	}
	if len(rec.Events) != 1 || rec.Events[0].Text != "a" {
		t.Errorf("saved events = %+v", rec.Events)
	}
	if r.Recording() {
		t.Error("still recording after finish")
	}
	// No recording starts while fennec-cua is stopped.
	r.Stopped = func() bool { return true }
	if _, err := r.Start(nil); err != ErrStopped {
		t.Errorf("start while stopped = %v", err)
	}
	// A recording that cannot start says why.
	r.Stopped = nil
	r.Listen = func(func(Input)) (func(), error) { return nil, errInputMonitoringTest }
	if _, err := r.Start(nil); err != errInputMonitoringTest {
		t.Errorf("start without the tap = %v", err)
	}
}

var errInputMonitoringTest = &os.PathError{Op: "tap", Path: "Input Monitoring", Err: os.ErrPermission}

func TestKeyRunsAndSecureInputAtStart(t *testing.T) {
	var k keyRun
	steps := []struct {
		text  string
		flags uint64
		first bool
	}{{"a", 0, true}, {"b", flagShift, false}, {"\t", 0, false}, {"c", 0, true}, {"s", flagCmd, false}, {"d", 0, true}}
	for i, s := range steps {
		if got := k.key(s.text, s.flags); got != s.first {
			t.Errorf("key %d %q first = %v", i, s.text, got)
		}
	}
	k.click()
	if !k.key("e", 0) {
		t.Error("a key after a click does not start a run")
	}
	if got := modsOf(flagCmd | flagShift | flagAlt | flagCtrl); !reflect.DeepEqual(got, []string{"cmd", "ctrl", "alt", "shift"}) {
		t.Errorf("mods = %v", got)
	}
	w := secureWatch{on: true}
	if w.turn(true) {
		t.Error("secure input already on at the start became a password event")
	}
	if w.turn(false) || !w.turn(true) {
		t.Error("a password field taking the focus later is no password event")
	}
}

func TestBundleStem(t *testing.T) {
	for path, want := range map[string]string{
		"/System/Applications/System Settings.app/Contents/MacOS/System Settings":                                 "System Settings",
		"/Applications/Google Chrome.app/Contents/Frameworks/x/Helpers/Google Chrome Helper.app/Contents/MacOS/h": "Google Chrome",
		"/usr/bin/git": "",
	} {
		if got := bundleStem(path); got != want {
			t.Errorf("bundleStem(%q) = %q, want %q", path, got, want)
		}
	}
	if got := BundleStem(os.Getpid()); got != "" && strings.Contains(got, "/") {
		t.Errorf("own bundle stem = %q", got)
	}
}
