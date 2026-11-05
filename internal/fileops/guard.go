package fileops

import (
	"context"
	"github.com/bojieli/agentreach/internal/reach"
	"io/fs"
)

// Guard checks every public I/O operation, including requests sent through an
// already open pipe/helper. Close remains available after a failed check.
func Guard(ops FileOps, check func(context.Context) error) FileOps { return &guardedOps{ops, check} }

type guardedOps struct {
	FileOps
	check func(context.Context) error
}

func (g *guardedOps) ready(ctx context.Context) error {
	if err := g.check(ctx); err != nil {
		_ = g.Close()
		return err
	}
	return nil
}
func (g *guardedOps) Read(ctx context.Context, p string, o, n int64) ([]byte, error) {
	if e := g.ready(ctx); e != nil {
		return nil, e
	}
	return g.FileOps.Read(ctx, p, o, n)
}
func (g *guardedOps) Write(ctx context.Context, p string, b []byte, m fs.FileMode) error {
	if e := g.ready(ctx); e != nil {
		return e
	}
	return g.FileOps.Write(ctx, p, b, m)
}
func (g *guardedOps) Stat(ctx context.Context, p string) (*reach.FileInfo, error) {
	if e := g.ready(ctx); e != nil {
		return nil, e
	}
	return g.FileOps.Stat(ctx, p)
}
func (g *guardedOps) List(ctx context.Context, p string) ([]reach.FileInfo, error) {
	if e := g.ready(ctx); e != nil {
		return nil, e
	}
	return g.FileOps.List(ctx, p)
}
func (g *guardedOps) Mkdir(ctx context.Context, p string, m fs.FileMode) error {
	if e := g.ready(ctx); e != nil {
		return e
	}
	return g.FileOps.Mkdir(ctx, p, m)
}
func (g *guardedOps) Remove(ctx context.Context, p string, r bool) error {
	if e := g.ready(ctx); e != nil {
		return e
	}
	return g.FileOps.Remove(ctx, p, r)
}
func (g *guardedOps) Rename(ctx context.Context, a, b string) error {
	if e := g.ready(ctx); e != nil {
		return e
	}
	return g.FileOps.Rename(ctx, a, b)
}
func (g *guardedOps) Search(ctx context.Context, r reach.SearchRequest) ([]reach.Match, error) {
	if e := g.ready(ctx); e != nil {
		return nil, e
	}
	return g.FileOps.Search(ctx, r)
}
func (g *guardedOps) Glob(ctx context.Context, p, q string) ([]string, error) {
	if e := g.ready(ctx); e != nil {
		return nil, e
	}
	return g.FileOps.Glob(ctx, p, q)
}
func (g *guardedOps) Hash(ctx context.Context, p string) (string, error) {
	if e := g.ready(ctx); e != nil {
		return "", e
	}
	return g.FileOps.Hash(ctx, p)
}
