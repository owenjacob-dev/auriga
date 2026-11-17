package app

import (
	"context"

	"github.com/owenjacob-dev/auriga/internal/corehttp"
	"github.com/owenjacob-dev/auriga/internal/deploy"
	"github.com/owenjacob-dev/auriga/internal/remote"
)

// liveOps implements deploy.Ops with the helper runner and the admin
// client of the live backend.
type liveOps struct{ b *LiveBackend }

var _ deploy.Ops = liveOps{}

func (o liveOps) Preflight(ctx context.Context, t deploy.Target) (remote.Preflight, error) {
	return o.b.runner.Preflight(ctx, t.Alias, t.ConfigPath, t.Service)
}

func (o liveOps) GenSeed(ctx context.Context, t deploy.Target) (string, error) {
	return o.b.runner.GenSeed(ctx, t.Alias, t.ConfigPath)
}

func (o liveOps) Stage(ctx context.Context, t deploy.Target) (remote.Staged, error) {
	src := deploy.SeedSourceOf(t)
	return o.b.runner.Stage(ctx, t.Alias, t.ConfigPath, t.Rendered, src, t.Import)
}

func (o liveOps) StageBackup(ctx context.Context, t deploy.Target) (string, error) {
	return o.b.runner.StageBackup(ctx, t.Alias, t.ConfigPath, t.RestoreFrom)
}

func (o liveOps) ReadStaged(ctx context.Context, t deploy.Target) (remote.RedactedConfig, error) {
	return o.b.runner.ReadConfig(ctx, t.Alias, t.ConfigPath+remote.StagedSuffix)
}

func (o liveOps) Backup(ctx context.Context, t deploy.Target) (string, error) {
	return o.b.runner.Backup(ctx, t.Alias, t.ConfigPath)
}

func (o liveOps) Install(ctx context.Context, t deploy.Target) (string, error) {
	return o.b.runner.Install(ctx, t.Alias, t.ConfigPath)
}

func (o liveOps) Init(ctx context.Context, t deploy.Target, i deploy.Init) error {
	return o.b.runner.Init(ctx, t.Alias, t.ConfigPath, t.Service, i.Kind, i.Archive)
}

func (o liveOps) Restart(ctx context.Context, t deploy.Target) (string, error) {
	return o.b.runner.Restart(ctx, t.Alias, t.Service)
}

func (o liveOps) Rollback(ctx context.Context, t deploy.Target, backup string) (string, error) {
	return o.b.runner.Rollback(ctx, t.Alias, t.ConfigPath, t.Service, backup)
}

func (o liveOps) Active(ctx context.Context, t deploy.Target) (string, error) {
	p, err := o.b.runner.Preflight(ctx, t.Alias, t.ConfigPath, t.Service)
	return p.Active, err
}

func (o liveOps) client(t deploy.Target) *corehttp.Client {
	return o.b.AdminClient(t.Alias, t.HTTPPort, t.ConfigPath, t.Service)
}

func (o liveOps) Info(ctx context.Context, t deploy.Target) (*corehttp.Info, error) {
	r, _, err := o.client(t).Info(ctx)
	if err != nil {
		return nil, err
	}
	return &r.Info, nil
}

func (o liveOps) Quorum(ctx context.Context, t deploy.Target) (*corehttp.QuorumResponse, error) {
	q, _, err := o.client(t).Quorum(ctx, true)
	return q, err
}
