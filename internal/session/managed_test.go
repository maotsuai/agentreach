package session

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestManagedContextAndLiveSessionCannotDowngrade(t *testing.T) {
	withTempHome(t)
	s := newTestSession(t, "same-name")
	s.Managed = &Managed{ProjectID: "original", RecordSHA256: "original-digest"}
	if err := s.Save(); err != nil {
		t.Fatal(err)
	}
	p, err := pathFor(s.Name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckManaged(context.Background()); err == nil {
		t.Fatal("deleted live binding remained valid")
	}
	local := newTestSession(t, s.Name)
	if err := local.Save(); err != nil {
		t.Fatal(err)
	}
	if err := s.CheckManaged(context.Background()); err == nil {
		t.Fatal("live binding accepted same-name local replacement")
	}
	t.Setenv("REACH_MANAGED", "1")
	t.Setenv("REACH_SESSION", s.Name)
	t.Setenv("REACH_MANAGED_PROJECT", s.Managed.ProjectID)
	t.Setenv("REACH_MANAGED_RECORD", s.Managed.RecordSHA256)
	if _, err := Load(s.Name); err == nil {
		t.Fatal("managed child loaded replacement local session")
	}
}

func TestDanglingSessionLinkIsCorruptionAndCannotBeOverwritten(t *testing.T) {
	withTempHome(t)
	dir, err := Dir()
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, "dangling.json")
	target := filepath.Join(canonicalTempDir(t), "offline", "session")
	if err := os.Symlink(target, p); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := Load("dangling"); err == nil || os.IsNotExist(err) {
		t.Fatalf("dangling link became empty slot: %v", err)
	}
	if err := newTestSession(t, "dangling").Save(); err == nil {
		t.Fatal("replaced dangling session link")
	}
	if got, err := os.Readlink(p); err != nil || got != target {
		t.Fatalf("evidence changed: %q %v", got, err)
	}
}

func TestManagedControlDescendantSymlinksAreRefusedBeforeWrite(t *testing.T) {
	for _, rel := range []string{"sessions", "projects", "conf", "terminals", filepath.Join("terminals", "demo.json"), filepath.Join("terminals", "demo.json.lock"), "codex-home", filepath.Join("codex-home", "guarded"), filepath.Join("conf", "guarded.claude-settings.json")} {
		t.Run(rel, func(t *testing.T) {
			base := canonicalTempDir(t)
			t.Setenv("REACH_HOME", base)
			p := filepath.Join(base, rel)
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				t.Fatal(err)
			}
			guest := canonicalTempDir(t)
			if err := os.Symlink(guest, p); err != nil {
				t.Skipf("symlink unavailable: %v", err)
			}
			s := newTestSession(t, "guarded")
			s.Managed = &Managed{ProjectID: "demo", RecordSHA256: "digest"}
			if err := s.Save(); err == nil {
				t.Fatal("managed write followed a control descendant link")
			}
			entries, err := os.ReadDir(guest)
			if err != nil || len(entries) != 0 {
				t.Fatalf("Guest obtained control output: %v %v", entries, err)
			}
		})
	}
}

func TestManagedControlHardlinkCannotShareAuthOrSettingsWithGuest(t *testing.T) {
	for _, rel := range []string{filepath.Join("terminals", "demo.json"), filepath.Join("codex-home", "guarded", "auth.json"), filepath.Join("conf", "guarded.claude-settings.json")} {
		t.Run(rel, func(t *testing.T) {
			base := canonicalTempDir(t)
			t.Setenv("REACH_HOME", base)
			p := filepath.Join(base, rel)
			if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
				t.Fatal(err)
			}
			guest := filepath.Join(canonicalTempDir(t), "guest-file")
			if err := os.WriteFile(guest, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Link(guest, p); err != nil {
				t.Skipf("hardlinks unavailable: %v", err)
			}
			s := newTestSession(t, "guarded")
			s.Managed = &Managed{ProjectID: "demo", RecordSHA256: "digest"}
			if err := s.Save(); err == nil {
				t.Fatal("managed writer accepted shared control leaf")
			}
			if b, err := os.ReadFile(guest); err != nil || string(b) != "keep" {
				t.Fatalf("Guest file changed: %q %v", b, err)
			}
		})
	}
}

func TestManagedFieldsRequireSchemaTwoAndCannotDowngrade(t *testing.T) {
	withTempHome(t)
	doc := rawSession("guarded", "posix")
	doc["version"] = 1
	doc["managed"] = map[string]any{"project_id": "demo", "record_sha256": "digest"}
	writeRawSession(t, "guarded", doc)
	if _, err := Load("guarded"); err == nil {
		t.Fatal("schema1 silently accepted security fields")
	}
	doc["version"] = 2
	writeRawSession(t, "guarded", doc)
	s, err := Load("guarded")
	if err != nil {
		t.Fatal(err)
	}
	s.Managed = nil
	if err := s.Save(); err == nil {
		t.Fatal("managed session downgraded")
	}
	loaded, err := Load("guarded")
	if err != nil || loaded.Managed == nil {
		t.Fatalf("binding lost: %v", err)
	}
}
func TestReadOnlySessionLookupDoesNotCreateState(t *testing.T) {
	root := filepath.Join(canonicalTempDir(t), "missing")
	t.Setenv("REACH_HOME", root)
	_, _ = Load("missing")
	_, _, _ = List()
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("lookup created state: %v", err)
	}
}
func TestSaveDoesNotOverwriteCorruptRecord(t *testing.T) {
	withTempHome(t)
	dir, _ := Dir()
	p := filepath.Join(dir, "bad.json")
	original := []byte("{broken")
	if err := os.WriteFile(p, original, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := newTestSession(t, "bad").Save(); err == nil {
		t.Fatal("overwrote corrupt session")
	}
	b, _ := os.ReadFile(p)
	if string(b) != string(original) {
		t.Fatal("corrupt evidence was replaced")
	}
}

func TestDuplicateManagedKeyCannotBecomeALegacySession(t *testing.T) {
	withTempHome(t)
	dir, _ := Dir()
	raw := `{"version":2,"name":"duplicate","target":{"kind":"local","workspace":"/tmp"},"mode":"exec","tier":"posix","managed":{"project_id":"demo","record_sha256":"invalid"},"managed":null}`
	if err := os.WriteFile(filepath.Join(dir, "duplicate.json"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load("duplicate"); err == nil {
		t.Fatal("duplicate managed key silently downgraded the session")
	}
}
