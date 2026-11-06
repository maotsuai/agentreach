package fileops_test

import (
	"context"
	"fmt"
	"github.com/bojieli/agentreach/internal/fileops"
	"github.com/bojieli/agentreach/internal/fileops/fileopstest"
	"github.com/bojieli/agentreach/internal/reach"
	"github.com/bojieli/agentreach/internal/rooted"
	"github.com/bojieli/agentreach/internal/transport"
	"github.com/bojieli/agentreach/internal/workspace"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type rootedLocalTransport struct {
	transport.Transport
	helper string
}

func (t rootedLocalTransport) Open(c context.Context, command string) (transport.Stream, error) {
	return t.Transport.Open(c, strings.Replace(command, rooted.HelperPath, t.helper, 1))
}
func (rootedLocalTransport) Run(context.Context, reach.ExecRequest) (reach.ExecResult, error) {
	return reach.ExecResult{}, fmt.Errorf("managed FileOps must not fall back to shell")
}
func TestManagedRootedFileOpsConformance(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("managed Guest is Linux")
	}
	helper := filepath.Join(t.TempDir(), "reach-helper")
	build := exec.Command("go", "build", "-o", helper, "../../cmd/reach-helper")
	if b, e := build.CombinedOutput(); e != nil {
		t.Fatalf("build helper: %v: %s", e, b)
	}
	local, e := transport.NewLocal()
	if e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	tr := rootedLocalTransport{local, helper}
	fileopstest.Run(t, root, func(t *testing.T) fileops.FileOps {
		t.Helper()
		ops, e := fileops.NewRooted(context.Background(), tr, workspace.Mapper{Root: root}, workspace.Guest, "conformance", &fileops.RootBinding{})
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { _ = ops.Close() })
		return ops
	})
}
