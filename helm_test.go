package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const (
	defaultHelloworldPort = 30030
	defaultSidecarPort    = 30031
	releaseName1          = "test1"
	defaultPollInterval   = 5 * time.Second
	yq                    = "bin/yq"
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

func (h *harness) installFluxChart(pollinterval time.Duration) {
	h.helmOrDie(context.Background(), "init", "--client-only")
	h.helmIgnoreErr(context.TODO(), "delete", "--purge", helmFluxRelease)
	// Hack until #1009 is fixed.
	h.helmIgnoreErr(context.TODO(), "delete", "--purge", releaseName1)
	h.helmOrDie(context.TODO(), "install",
		"--set", "helmOperator.create=true",
		"--set", "git.url="+h.gitURL(),
		"--set", "git.chartsPath=charts",
		"--set", "image.tag=latest",
		"--set", "helmOperator.tag=latest",
		"--set", "git.pollInterval="+pollinterval.String(),
		"--name", helmFluxRelease,
		"--namespace", fluxNamespace,
		"helm/charts/weave-flux")
}

func (h *harness) gitAddCommitPushSync() {
	ctx, cancel := context.WithTimeout(context.Background(), syncTimeout)
	h.gitOrDie(ctx, "add", h.repodir)
	h.gitOrDie(ctx, "commit", "-m", "Deploy helloworld")
	h.gitOrDie(ctx, "push", "-u", "origin", "master")
	h.waitForSync(ctx, "HEAD")
	cancel()
}

func (h *harness) pushNewHelmFluxRepo(ctx context.Context) {
	execNoErr(ctx, h.t, "cp", "-rT", "helm/repo", h.repodir)
	h.gitAddCommitPushSync()
}

func (h *harness) initHelmTest(pollinterval time.Duration) {
	h.setupGitRemote()
	h.installFluxChart(pollinterval)
	h.initGitRepoLocal(context.TODO())
	h.pushNewHelmFluxRepo(context.Background())
}

func (h *harness) exitif(err error) {
	if err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) lastHelmRelease(releaseName string) helmHistory {
	// There may be one or two history entries, depending on timing.  It
	// seems there's an unnecessary upgrade happening, but only once.
	histstr := h.helmOrDie(context.Background(), "history", releaseName1, "-ojson")
	var hist []helmHistory
	h.exitif(json.Unmarshal([]byte(histstr), &hist))
	if len(hist) == 0 || hist[len(hist)-1].Status == "" {
		h.t.Errorf("error getting helm history, raw output: %q", histstr)
	}
	return hist[len(hist)-1]
}

func (h *harness) helmReleaseDeployed(hist helmHistory, releaseName string, minRevision int) error {
	if hist.Revision < minRevision {
		return fmt.Errorf("helm release revision of %q is %d, smaller than our min of %d", releaseName, hist.Revision, minRevision)
	}
	if hist.Status != "DEPLOYED" {
		return fmt.Errorf("helm release status of %q is %q rather than DEPLOYED", releaseName, hist.Status)
	}
	return nil
}

func (h *harness) helmReleaseHasValue(releaseName string, minRevision int, key, val string) error {
	hist := h.lastHelmRelease(releaseName)
	if err := h.helmReleaseDeployed(hist, releaseName, minRevision); err != nil {
		return err
	}

	valstr := h.helmOrDie(context.Background(), "get", "values", releaseName,
		"--revision", fmt.Sprintf("%d", hist.Revision))
	yqout := strings.TrimSpace(strOrDie(
		envExecStdin(context.Background(), h.t, nil, strings.NewReader(valstr), yq, "r", "-", key)))
	if val != yqout {
		return fmt.Errorf("expected value for %q is %q, got %q", key, val, yqout)
	}
	return nil
}

func (h *harness) assertHelmReleaseDeployed(releaseName string, minRevision int) int {
	var hist helmHistory
	ctx, cancel := context.WithTimeout(context.Background(), releaseTimeout)
	defer cancel()
	h.exitif(until(ctx, func(ictx context.Context) error {
		hist = h.lastHelmRelease(releaseName)
		return h.helmReleaseDeployed(hist, releaseName, minRevision)
	}))
	return hist.Revision
}

func (h *harness) assertHelmReleaseHasValue(timeout time.Duration, releaseName string, minRevision int, key, val string) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	h.exitif(until(ctx, func(ictx context.Context) error {
		return h.helmReleaseHasValue(releaseName, minRevision, key, val)
	}))
}

func (h *harness) serviceReturns(port int, expected string) error {
	url := fmt.Sprintf("http://%s:%d", h.clusterIP, port)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return until(ctx, func(ictx context.Context) error {
		got, err := httpget(ictx, url)
		if err != nil || got != expected {
			return fmt.Errorf("service check on %d failed, got %q, error: %v", port, got, err)
		}
		return nil
	})
}

func (h *harness) assertServiceReturns(port int, expected string) {
	if err := h.serviceReturns(port, expected); err != nil {
		h.t.Error(err)
	}
}

func (h *harness) updateGitYaml(relpath string, yamlpath string, value string) {
	execNoErr(context.Background(), h.t, yq, "w", "-i",
		filepath.Join(h.repodir, relpath), yamlpath, value)
}

func TestChart(t *testing.T) {
	h := newharness(t)
	h.initHelmTest(defaultPollInterval)

	h.assertHelmReleaseDeployed(releaseName1, 1)

	h.assertServiceReturns(defaultHelloworldPort, "Ahoy\n")
	h.assertServiceReturns(defaultSidecarPort, "I am a sidecar\n")
}

func TestChartUpdateViaGit(t *testing.T) {
	h := newharness(t)
	h.initHelmTest(defaultPollInterval)

	initialRevision := h.assertHelmReleaseDeployed(releaseName1, 1)
	h.assertServiceReturns(defaultHelloworldPort, "Ahoy\n")
	h.assertServiceReturns(defaultSidecarPort, "I am a sidecar\n")

	// obviously this should work if the above works, it's just to
	// contrast with the Dial invocation below
	_, err := net.Dial("tcp", fmt.Sprintf("%s:%d", h.clusterIP, defaultSidecarPort))
	h.exitif(err)

	newMessage := "salut"
	newSidecarPort := defaultSidecarPort + 2
	h.updateGitYaml("releases/helloworld.yaml", "spec.values.hellomessage", newMessage)
	h.updateGitYaml("releases/helloworld.yaml", "spec.values.service.sidecar.port",
		fmt.Sprintf("%d", newSidecarPort))
	h.gitAddCommitPushSync()

	h.assertHelmReleaseDeployed(releaseName1, initialRevision+1)
	h.assertServiceReturns(defaultHelloworldPort, newMessage+"\n")
	h.assertServiceReturns(newSidecarPort, "I am a sidecar\n")

	_, err = net.Dial("tcp", fmt.Sprintf("%s:%d", h.clusterIP, defaultSidecarPort))
	if err == nil {
		t.Errorf("old sidecar port %d still open", defaultSidecarPort)
	}
}

func TestChartUpdateViaHelm(t *testing.T) {
	h := newharness(t)
	pollInterval := 20 * time.Second
	h.initHelmTest(pollInterval)

	initialRevision := h.assertHelmReleaseDeployed(releaseName1, 1)
	h.assertServiceReturns(defaultHelloworldPort, "Ahoy\n")
	h.assertServiceReturns(defaultSidecarPort, "I am a sidecar\n")

	key, val := "hellomessage", "greetings"
	h.helmOrDie(context.TODO(), "upgrade", releaseName1,
		filepath.Join(h.repodir, "charts", "helloworld"),
		"--reuse-values",
		"--set", fmt.Sprintf("%s=%s", key, val))

	h.assertHelmReleaseHasValue(releaseTimeout, releaseName1, initialRevision+1, key, val)
	h.assertServiceReturns(defaultHelloworldPort, val+"\n")

	// TODO specify minrevision more precisely
	h.assertHelmReleaseHasValue(releaseTimeout+pollInterval, releaseName1, initialRevision+1, key, "null")
	h.assertServiceReturns(defaultHelloworldPort, "Ahoy\n")
}

// TODO tests:
// - chart with README template
// - deploy chart directly via helm, verify not touched by flux
