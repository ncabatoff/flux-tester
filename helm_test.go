package main

import (
	"context"
	"testing"
)

func (h *harness) installFluxChart() {
	h.helmOrDie(context.Background(), "init", "--client-only")
	h.helmIgnoreErr(context.TODO(), "delete", "--purge", helmFluxRelease)
	h.helmOrDie(context.TODO(), "install",
		"--set", "helmOperator.create=true",
		"--set", "git.url="+h.gitURL(),
		"--set", "git.chartsPath=charts",
		"--set", "image.tag=latest",
		"--set", "helmOperator.tag=latest",
		"--name", helmFluxRelease,
		"--namespace", fluxNamespace,
		"helm/charts/weave-flux")
}

// TODO test chart with README template
func (h *harness) deployChartViaGit(ctx context.Context) {
	execNoErr(ctx, h.t, "cp", "-rT", "helm/repo", h.repodir)
	h.gitOrDie(ctx, "add", h.repodir)
	h.gitOrDie(ctx, "commit", "-m", "Deploy helloworld")
	h.gitOrDie(ctx, "push", "-u", "origin", "master")
}
func TestChart(t *testing.T) {
	h := newharness(t)
	h.setupGitRemote()
	h.installFluxChart()
	h.initGitRepoLocal(context.TODO())
	h.deployChartViaGit(context.Background())
	ctx, cancel := context.WithTimeout(context.Background(), syncTimeout)
	h.waitForSync(ctx, "HEAD")
	cancel()
}
