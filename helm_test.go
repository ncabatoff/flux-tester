package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
)

const (
	defaultHelloworldPort = 30030
	defaultSidecarPort    = 30031
)

type (
	helmHistory struct {
		Chart       string `json:"chart"`
		Description string `json:"description"`
		Revision    int    `json:"revision"`
		Status      string `json:"status"`
		Updated     string `json:"updated"`
	}
)

func (h *harness) installFluxChart() {
	h.helmOrDie(context.Background(), "init", "--client-only")
	h.helmIgnoreErr(context.TODO(), "delete", "--purge", helmFluxRelease)
	// Hack until #1009 is fixed.
	h.helmIgnoreErr(context.TODO(), "delete", "--purge", "test1")
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

func (h *harness) deployChartViaGit(ctx context.Context) {
	execNoErr(ctx, h.t, "cp", "-rT", "helm/repo", h.repodir)
	h.gitOrDie(ctx, "add", h.repodir)
	h.gitOrDie(ctx, "commit", "-m", "Deploy helloworld")
	h.gitOrDie(ctx, "push", "-u", "origin", "master")
}

func (h *harness) exitif(err error) {
	if err != nil {
		h.t.Fatal(err)
	}
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

	// There may be one or two history entries, depending on timing.  It
	// seems there's an unnecessary upgrade happening, but only once.
	histstr := h.helmOrDie(context.Background(), "history", "test1", "-ojson")
	var hist []helmHistory
	h.exitif(json.Unmarshal([]byte(histstr), &hist))
	if len(hist) < 1 || len(hist) > 2 {
		h.t.Errorf("expected 1 or 2 history entries, got %+v (raw: %q)", hist, histstr)
	}
	if hist[len(hist)-1].Status != "DEPLOYED" {
		h.t.Errorf("expected last history entry to have status DEPLOYED, got %+v (raw: %q)", hist[len(hist)-1], histstr)
	}

	hwout, err := httpgetNoErr(context.TODO(), fmt.Sprintf("http://%s:%d", h.clusterIP, defaultHelloworldPort))
	if err != nil || hwout != "Ahoy\n" {
		h.t.Errorf("helloworld service check failed, got %q, error: %v", hwout, err)
	}
	scout, err := httpgetNoErr(context.TODO(), fmt.Sprintf("http://%s:%d", h.clusterIP, defaultSidecarPort))
	if err != nil || scout != "I am a sidecar\n" {
		h.t.Errorf("sidecar service check failed, got %q, error: %v", scout, err)
	}
}

// TODO tests:
// - chart with README template
// - deploy chart directly via helm, verify not touched by flux
// - change managed chart via helm, verify reverted by flux
// - change managed chart via git, verify updated by flux
