package link

import (
	"context"
	"errors"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lawsmd/slate"
)

// fake is a document without a browser.
type fake struct {
	mu       sync.Mutex
	text     string
	rev      uint64
	last     slate.Edit // the last edit asked for
	watchers []func(slate.Event)
}

func (f *fake) Target() string { return "jira" }

func (f *fake) Snapshot(context.Context) (slate.Snapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slate.Snapshot{Markdown: f.text, Rev: f.rev, Selected: "sel"}, nil
}

func (f *fake) Edit(_ context.Context, e slate.Edit, origin string) (uint64, error) {
	f.mu.Lock()
	f.last = e
	if e.IfRev != 0 && e.IfRev != f.rev {
		f.mu.Unlock()
		return 0, errors.New("the document has changed")
	}
	if !e.Own && strings.Count(f.text, e.Old) != 1 {
		f.mu.Unlock()
		return 0, errors.New("the text to replace is not in the document")
	}
	f.text = strings.Replace(f.text, e.Old, e.New, 1)
	f.rev++
	rev, watchers := f.rev, f.watchers
	f.mu.Unlock()
	for _, w := range watchers {
		w(slate.Changed{Rev: rev, Origin: origin})
	}
	return rev, nil
}

func (f *fake) Watch(fn func(slate.Event)) func() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.watchers = append(f.watchers, fn)
	return func() {}
}

func TestLink(t *testing.T) {
	// A Unix socket's path is short on every system, so the test meets in
	// a directory with a short name.
	d, err := os.MkdirTemp("", "sl")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(d)
	t.Setenv(dirEnv, d)

	if all, err := List(); err != nil || len(all) != 0 {
		t.Fatalf("before any host: %+v, %v", all, err)
	}
	if _, err := Read(""); err == nil {
		t.Fatal("reading with no editor open should fail")
	}

	doc := &fake{text: "teh comment"}
	reg, err := Register(doc, Info{Title: "comment on PROJ-1", Meta: map[string]string{"ticket": "PROJ-1"}})
	if err != nil {
		t.Fatal(err)
	}

	all, err := List()
	if err != nil || len(all) != 1 {
		t.Fatalf("list: %+v, %v", all, err)
	}
	if e := all[0]; e.ID != reg.ID() || e.Title != "comment on PROJ-1" || e.Target != "jira" ||
		e.Meta["ticket"] != "PROJ-1" || e.PID != os.Getpid() {
		t.Fatalf("entry: %+v", e)
	}

	// With one editor open the ID may be left out.
	snap, err := Read("")
	if err != nil || snap.Markdown != "teh comment" || snap.Selected != "sel" {
		t.Fatalf("read: %+v, %v", snap, err)
	}

	changes := make(chan Change, 1)
	watching := make(chan error, 1)
	go func() {
		watching <- Watch(reg.ID(), func(c Change) bool {
			changes <- c
			return false
		})
	}()
	// The watch has to be in place before the edit it should report.
	deadline := time.Now().Add(5 * time.Second)
	for {
		doc.mu.Lock()
		n := len(doc.watchers)
		doc.mu.Unlock()
		if n > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the watch did not start")
		}
		time.Sleep(5 * time.Millisecond)
	}

	rev, err := Edit(reg.ID(), slate.Edit{Old: "teh", New: "the"}, "claude")
	if err != nil || rev != 1 {
		t.Fatalf("edit: revision %d, %v", rev, err)
	}
	select {
	case c := <-changes:
		if c.Rev != 1 || c.Origin != "claude" {
			t.Fatalf("watched change: %+v", c)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("the watch reported nothing")
	}
	if err := <-watching; err != nil {
		t.Fatal(err)
	}
	if _, err := Edit(reg.ID(), slate.Edit{Old: "absent", New: "x"}, "claude"); err == nil || !strings.Contains(err.Error(), "not in the document") {
		t.Fatalf("an edit that cannot apply should carry the editor's reason: %v", err)
	}
	if _, err := Edit(reg.ID(), slate.Edit{Old: "the", New: "a", IfRev: 9}, "claude"); err == nil {
		t.Fatal("an edit at a revision the document has left should fail")
	}
	if _, err := Edit(reg.ID(), slate.Edit{Own: true, New: "x", IfRev: 1}, "claude"); err != nil {
		t.Fatal(err)
	}
	doc.mu.Lock()
	last := doc.last
	doc.mu.Unlock()
	if !last.Own || last.IfRev != 1 || last.New != "x" {
		t.Fatalf("the edit reached the document as %+v", last)
	}
	if _, err := Read("nobody-9"); err == nil {
		t.Fatal("reading an unknown editor should fail")
	}

	second, err := Register(&fake{text: "other"}, Info{Title: "notes"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Read(""); err == nil || !strings.Contains(err.Error(), "several") {
		t.Fatalf("with two editors open the ID is needed: %v", err)
	}
	if err := reg.Describe(Info{Title: "comment on PROJ-2"}); err != nil {
		t.Fatal(err)
	}
	if e, err := Find(reg.ID()); err != nil || e.Title != "comment on PROJ-2" {
		t.Fatalf("after Describe: %+v, %v", e, err)
	}

	second.Close()
	reg.Close()
	if all, _ := List(); len(all) != 0 {
		t.Fatalf("after closing: %+v", all)
	}
	if left, _ := os.ReadDir(d); len(left) != 0 {
		t.Fatalf("files left behind: %v", left)
	}

	// A host that died leaves its files. The next listing removes them.
	os.WriteFile(listingPath(d, 1), []byte(`{"pid":1,"address":"`+strings.ReplaceAll(d, `\`, `\\`)+`/1.sock","editors":[]}`), 0o600)
	if all, _ := List(); len(all) != 0 {
		t.Fatalf("a dead host listed: %+v", all)
	}
	if left, _ := os.ReadDir(d); len(left) != 0 {
		t.Fatalf("a dead host's files were kept: %v", left)
	}
}

func TestOff(t *testing.T) {
	t.Setenv(offEnv, "0")
	if _, err := Register(&fake{}, Info{}); err == nil {
		t.Fatal("registering with the link turned off should fail")
	}
}
