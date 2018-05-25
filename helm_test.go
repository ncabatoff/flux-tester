package main

import (
	"context"
	"testing"
)

func (h *harness) installFluxChart() {
	// reponame := "sp"
	relname := "cd"

	h.helmOrDie(context.Background(), "init", "--client-only")
	// h.helmOrDie(context.Background(), "repo", "add", reponame, "https://stefanprodan.github.io/k8s-podinfo")
	h.helmIgnoreErr(context.TODO(), "delete", "--purge", relname)
	h.helmOrDie(context.TODO(), "install",
		"--set", "helmOperator.create=true",
		"--set", "git.url="+h.gitURL(),
		"--set", "git.chartsPath=charts",
		"--set", "image.tag=latest",
		"--set", "helmOperator.tag=latest",
		"--name", relname,
		"--namespace", helmFluxNamespace,
		"helm/charts/weave-flux")
}

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
	h.verifySyncAndSvcs(t, "HEAD", helloworldImageTag, sidecarImageTag)
}
