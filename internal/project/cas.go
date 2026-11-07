package project

import (
	"context"
	"fmt"
	"math"
	"time"

	"github.com/bojieli/agentreach/internal/hostpath"
	"github.com/bojieli/agentreach/internal/state"
)

// Locked exists only during WithCAS. Publication is atomic at the projects
// record; side records can never override its authority after a crash.
type Locked struct {
	record Record
	path   string
	active bool
	write  func(string, any) error
	ctx    context.Context
}

// Current returns the original authorization held under the project lock.
func (l *Locked) Current() Record { return l.record }

// Publish commits one explicit migration or execution rebind. The lock order
// is project -> runtime journal -> policy; normal policy never takes this lock.
func (l *Locked) Publish(next Record) error {
	if !l.active {
		return fmt.Errorf("policy_denied: project lease is no longer active")
	}
	if err := l.ctx.Err(); err != nil {
		return err
	}
	old, err := Load(l.record.Spec.ID)
	if err != nil {
		return err
	}
	if Digest(old) != Digest(l.record) {
		return fmt.Errorf("plan_stale: project changed before publication")
	}
	if err := next.Validate(); err != nil {
		return err
	}
	if l.record.Binding.Revision == math.MaxUint64 || next.Binding.Revision != l.record.Binding.Revision+1 || next.Spec.ID != old.Spec.ID || next.Spec.Schema != SchemaV2 {
		return fmt.Errorf("policy_denied: invalid revision transition")
	}
	migration := old.Spec.Schema == Schema && next.Binding.Generation == old.Binding.Generation
	rebind := old.Spec.Schema == SchemaV2 && old.Binding.Generation != math.MaxUint64 && next.Binding.Generation == old.Binding.Generation+1
	expected := old
	expected.Spec.Schema, expected.Binding.Schema = SchemaV2, SchemaV2
	expected.Binding.Revision++
	if rebind {
		expected.Spec.Container = next.Binding.ContainerID
		expected.Binding.Generation++
		expected.Binding.ContainerID, expected.Binding.StartedAt = next.Binding.ContainerID, next.Binding.StartedAt
		expected.Binding.SnapshotSHA256 = next.Binding.SnapshotSHA256
		if expected.Binding.ContainerID == old.Binding.ContainerID && expected.Binding.StartedAt == old.Binding.StartedAt {
			return fmt.Errorf("policy_denied: rebind requires a new execution identity")
		}
	}
	if !migration && !rebind || Digest(expected) != Digest(next) {
		return fmt.Errorf("policy_denied: transition changes unapproved project configuration")
	}
	if err := hostpath.Canonical(l.path, false); err != nil {
		return err
	}
	if err := hostpath.PrivateFile(l.path); err != nil {
		return err
	}
	if err := l.write(l.path, next); err != nil {
		return err
	}
	l.record = next
	return nil
}

// WithCAS checks both the complete digest and revision only after acquiring the
// same non-stealing lock used for registration. Reads never acquire this lock.
func WithCAS(ctx context.Context, id, expectedDigest string, expectedRevision uint64, fn func(*Locked) error) error {
	p, err := recordPath(id)
	if err != nil {
		return err
	}
	if _, err = Load(id); err != nil {
		return err
	}
	if err = hostpath.Canonical(p+".lock", true); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	wait, stop := context.WithTimeout(ctx, 3*time.Second)
	u, err := state.Lock(wait, p+".lock")
	stop()
	if err != nil {
		return err
	}
	defer u()
	r, err := Load(id)
	if err != nil {
		return err
	}
	if Digest(r) != expectedDigest || r.Binding.Revision != expectedRevision {
		return fmt.Errorf("plan_stale: expected project digest/revision no longer current")
	}
	l := &Locked{record: r, path: p, active: true, write: state.Write, ctx: ctx}
	defer func() { l.active = false }()
	return fn(l)
}
