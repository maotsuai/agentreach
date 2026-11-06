//go:build linux

package terminalguest

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bojieli/agentreach/internal/terminal"
)

func logFixture(t *testing.T) (*os.Root, metadata, progress) {
	t.Helper()
	id, e := terminal.NewID()
	if e != nil {
		t.Fatal(e)
	}
	epoch, _ := terminal.NewID()
	nonce, _ := terminal.NewID()
	r, e := openDir(id, true)
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { r.Close(); os.RemoveAll(filepath.Join(baseDir(), id)) })
	m := metadata{Schema: 1, Identity: terminal.Identity{ID: id, Epoch: epoch, Nonce: nonce, Binding: strings.Repeat("a", 64), Server: "boot:99999999:1", Session: "$0", Pane: "%0"}}
	if e = save(r, "identity.json", m); e != nil {
		t.Fatal(e)
	}
	f, e := r.OpenFile("output", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = f.Write([]byte("中\x00\xff")); e != nil {
		t.Fatal(e)
	}
	fi, e := f.Stat()
	if e != nil {
		t.Fatal(e)
	}
	f.Close()
	p := progress{Offset: 5, Log: fileIdentity(fi), Collector: "boot:99999999:1", Done: true}
	if e = save(r, "progress.json", p); e != nil {
		t.Fatal(e)
	}
	return r, m, p
}
func TestDrainRequiresBothNaturalExitAndDurableCollectorEOF(t *testing.T) {
	r, m, p := logFixture(t)
	o, e := observe(context.Background(), r, m)
	if e != nil || o.Complete || o.Gap != "exit_unconfirmed" {
		t.Fatal(o, e)
	}
	if e = save(r, "exit.json", exitEvidence{Kind: "pane_exited"}); e != nil {
		t.Fatal(e)
	}
	p.Done = false
	save(r, "progress.json", p)
	o, e = observe(context.Background(), r, m)
	if e != nil || o.Complete || o.State != "exited" || o.Gap != "" {
		t.Fatal(o, e)
	}
	p.Done = true
	save(r, "progress.json", p)
	o, e = observe(context.Background(), r, m)
	if e != nil || !o.Complete || o.ExitCode != nil {
		t.Fatal(o, e)
	}
	o, e = operate(context.Background(), terminal.GuestRequest{Op: "read", Identity: m.Identity, Offset: 0, Limit: 1})
	if e != nil || len(o.Data) != 1 || o.Data[0] != 0xe4 || o.Offset != 1 {
		t.Fatal(o, e)
	}
	if e = save(r, "close-intent.json", true); e != nil {
		t.Fatal(e)
	}
	o, e = observe(context.Background(), r, m)
	if e != nil || o.State == "closed" || o.Complete || o.Gap != "closed_before_drain" {
		t.Fatal("close intent falsely confirmed", o, e)
	}
	if e = save(r, "closed.json", true); e != nil {
		t.Fatal(e)
	}
	o, e = observe(context.Background(), r, m)
	if e != nil || o.Complete || o.Gap != "closed_before_drain" {
		t.Fatal(o, e)
	}
}
func TestLogIdentityTruncationAndCollectorFaultAreExplicit(t *testing.T) {
	r, m, p := logFixture(t)
	p.Done = false
	save(r, "progress.json", p)
	o, e := observe(context.Background(), r, m)
	if e != nil || o.Gap != "collector_failed" {
		t.Fatal(o, e)
	}
	f, e := r.OpenFile("output", os.O_TRUNC|os.O_WRONLY, 0600)
	if e != nil {
		t.Fatal(e)
	}
	f.Close()
	o, e = observe(context.Background(), r, m)
	if e != nil || o.Gap != "log_changed" || o.Offset != 5 || o.Complete {
		t.Fatal(o, e)
	}
	if e = r.Remove("output"); e != nil {
		t.Fatal(e)
	}
	o, e = observe(context.Background(), r, m)
	if e != nil || o.Gap != "log_missing" {
		t.Fatal(o, e)
	}
}
func TestControlLeavesRefuseLinksAndProgressBudget(t *testing.T) {
	r, m, p := logFixture(t)
	if e := r.Symlink("identity.json", "unsafe.json"); e != nil {
		t.Fatal(e)
	}
	if e := save(r, "unsafe.json", true); e == nil {
		t.Fatal("replaced control symlink")
	}
	if e := r.Link("identity.json", "hard.json"); e != nil {
		t.Fatal(e)
	}
	var v metadata
	if e := load(r, "hard.json", &v); e == nil {
		t.Fatal("read shared control leaf")
	}
	p.Offset = -1
	save(r, "progress.json", p)
	if _, e := observe(context.Background(), r, m); e == nil {
		t.Fatal("negative progress accepted")
	}
}
