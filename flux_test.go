// +build integration_test

package test

import (
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/weaveworks/flux/image"
)

const (
	k8sSetupTimeout   = 600 * time.Second
	imageSetupTimeout = 30 * time.Second
	gitSetupTimeout   = 10 * time.Second
	syncTimeout       = 60 * time.Second
	// releaseTimeout is how long we allow between seeing sync done and seeing
	// a change made to a helm release.
	releaseTimeout          = 30 * time.Second
	automationUpdateTimeout = 180 * time.Second
	fluxPort                = "30080"
	gitRepoPath             = "/git-server/repos/repo.git"
	helloworldImageTag      = "master-a000001"
	sidecarImageTag         = "master-a000001"
	appNamespace            = "default"
	fluxSyncTag             = "flux-sync"
)

type (
	// harness holds state that may be test-specific.
	harness struct {
		clusterIP string
		t         *testing.T
		repodir   string
		clusterAPI
		gitAPI
		helmAPI
	}
)

var (
	helloworldImageName = image.Name{Domain: "quay.io", Image: "weaveworks/helloworld"}
	sidecarImageName    = image.Name{Domain: "quay.io", Image: "weaveworks/sidecar"}
)

func newharness(t *testing.T) *harness {
	testdir := filepath.Join(global.testroot, t.Name())
	os.Mkdir(testdir, 0755)

	repodir := filepath.Join(testdir, "repo")
	h := &harness{
		repodir:    repodir,
		t:          t,
		clusterIP:  global.clusterIP,
		clusterAPI: minikube{mt: global.clusterAPI.(minikube).mt, lg: t},
		helmAPI:    helm{ht: global.helmAPI.(helm).ht, lg: t},
	}

	// Create configmap for our public key
	pubkeyConfigMap := "ssh-public-keys"
	global.kubectlAPI.delete(fluxNamespace, "configmap", pubkeyConfigMap)
	global.must(global.kubectlAPI.create(fluxNamespace, "configmap", pubkeyConfigMap, "--from-file",
		fmt.Sprintf("me.pub=%s", global.sshKeyFilePublic())))

	// Create secret for our private key
	secretName := "flux-git-deploy"
	global.kubectlAPI.delete(fluxNamespace, "secret", secretName)
	global.must(global.kubectlAPI.create(fluxNamespace, "secret", "generic", secretName, "--from-file",
		fmt.Sprintf("identity=%s", global.sshKeyFilePrivate())))

	// Install git service, which depends on the public key
	h.installGitChart()
	portOpen(context.Background(), h.clusterIP, 30022)

	// Get the ssh host id
	knownHostsContent := execNoErr(context.TODO(), nil, "ssh-keyscan", "-p", "30022", global.clusterIP)
	ioutil.WriteFile(global.knownHostsPath(), []byte(knownHostsContent), 0600)

	// Record ssh host id in configmap for flux to use
	configMapName := "ssh-known-hosts"
	global.kubectlAPI.delete(fluxNamespace, "configmap", configMapName)
	global.must(global.kubectlAPI.create(fluxNamespace, "configmap", configMapName, "--from-file",
		fmt.Sprintf("known_hosts=%s", global.knownHostsPath())))

	// Now setup our local clone of the ssh repo.
	h.gitAPI = mustNewGit(t, repodir,
		fmt.Sprintf(`ssh -i %s -o StrictHostKeyChecking=yes -o UserKnownHostsFile=%s`,
			global.sshKeyFilePrivate(), global.knownHostsPath()), h.gitURL())

	return h
}

func (h *harness) gitURL() string {
	return fmt.Sprintf("ssh://git@%s:30022%s", h.clusterIP, gitRepoPath)
}

func (h *harness) fluxURL() string {
	u := &url.URL{Scheme: "http", Host: h.clusterIP + ":" + fluxPort, Path: "/api/flux"}
	return u.String()
}

func (h *harness) must(err error) {
	h.t.Helper()
	if err != nil {
		h.t.Fatal(err)
	}
}

func writeTemplate(destdir, tplpath string, values interface{}) (string, error) {
	tpl, err := template.ParseFiles(tplpath)
	if err != nil {
		return "", fmt.Errorf("Unable to parse template %q: %v", tplpath, err)
	}

	base := filepath.Base(tplpath)
	foutpath := filepath.Join(destdir, strings.TrimSuffix(base, ".tpl"))
	fout, err := os.Create(foutpath)
	if err != nil {
		return "", fmt.Errorf("Unable to write template output %q: %v", foutpath, err)
	}

	err = tpl.ExecuteTemplate(fout, base, values)
	if err != nil {
		return "", fmt.Errorf("Unable to execute template %q: %v", tplpath, err)
	}
	err = fout.Close()
	if err != nil {
		return "", fmt.Errorf("Unable to close deployment %q: %v", foutpath, err)
	}
	return foutpath, nil
}

func writeHelloWorldDeployment(destdir string) (string, error) {
	return writeTemplate(destdir, "nohelm/helloworld-deployment.yaml.tpl",
		struct{ ImageTag string }{helloworldImageTag})
}

// func writeFluxDeployment(destdir string, giturl string) (string, error) {
// 	return writeTemplate(destdir, "nohelm/flux-deploy-all.yaml.tpl",
// 		struct {
// 			FluxImage string
// 			FluxPort  string
// 			GitURL    string
// 		}{fluxImage, fluxPort, giturl})
// }

func (h *harness) deployViaGit(ctx context.Context) {
	log.Printf("deploying hello world via git")
	_, err := writeHelloWorldDeployment(h.repodir)
	if err != nil {
		h.t.Fatal(err)
	}
	h.mustAddCommitPush()
}

func (h *harness) waitForSync(ctx context.Context, targetRevSource string) {
	h.t.Helper()
	h.must(until(ctx, func(ictx context.Context) error {
		h.mustFetch()
		targetRev, err := h.revlist("-n", "1", targetRevSource)
		if err != nil {
			h.t.Fatalf("Unable to get latest rev for %s: %v", targetRevSource, err)
		}
		syncRev, _ := h.revlist("-n", "1", fluxSyncTag)
		if syncRev != targetRev {
			return fmt.Errorf("sync tag %q points at %q instead of target %s",
				fluxSyncTag, syncRev, targetRev)
		}
		return nil
	}))
}

func (h *harness) waitForUpstreamCommits(ctx context.Context, mincount int) {
	h.must(until(ctx, func(ictx context.Context) error {
		h.mustFetch()
		strcount, _ := h.revlist("--count", "HEAD.."+fluxSyncTag)
		if strcount == "" {
			return fmt.Errorf("no output returned by git revlist")
		}
		count, err := strconv.Atoi(strings.TrimSpace(strcount))
		if err != nil {
			h.t.Fatalf("git rev-list --count returned a non-numeric output %q: %v", strcount, err)
		}
		if count < mincount {
			return fmt.Errorf("Found %d commits instead of required minimum %d", count, mincount)
		}
		return nil
	}))
}

func (h *harness) automate() {
	// In this case, unlike services() we'll invoke fluxctl to enable automation.  From looking at the fluxctl
	// source there's more going on than a simple API call.  And it's not like we have to parse the output.

	execNoErr(context.TODO(), h.t, "fluxctl", "--url", h.fluxURL(), "automate",
		fmt.Sprintf("--controller=%s:deployment/helloworld", appNamespace))
}

func (h *harness) applyFlux() {
	// For now we've abandoned the original helmless approach used in flux's test/bin/test-flux;
	// it complicates things to have to support both that and the install via helm chart, and it
	// doesn't buy us anything.
	h.installFluxChart(defaultPollInterval)

	// h.kubectlIgnoreErrs(context.TODO(), h.t, fluxNamespace, "delete", "deploy", "flux", "memcached")
	// out, err := writeFluxDeployment(h.repodir, h.gitURL())
	// if err != nil {
	// 	h.t.Fatal(err)
	// }
	// h.kubectlOrDie(context.TODO(), h.t, fluxNamespace, "apply", "-f", out)
}

func (h *harness) verifySyncAndSvcs(t *testing.T, targetRevSource, expectedHelloworldTag string, expectedSidecarTag string) {
	expected := map[string]image.Ref{
		"helloworld": image.Ref{helloworldImageName, expectedHelloworldTag},
		"sidecar":    image.Ref{sidecarImageName, expectedSidecarTag},
	}

	var (
		diff string
		got  map[string]image.Ref
	)

	log.Printf("Waiting %v for sync tag to be current", syncTimeout)
	ctx, cancel := context.WithTimeout(context.Background(), syncTimeout)
	h.waitForSync(ctx, targetRevSource)
	for got == nil || diff != "" {
		got = fluxServices(ctx, h.fluxURL(), t, appNamespace, appNamespace+":deployment/helloworld")
		diff = cmp.Diff(got, expected)
	}
	cancel()

	if diff != "" {
		t.Errorf("Expected %+v, got %+v, diff: %s", expected, got, diff)
	}
}

// TestSync makes sure that the sync tag has been updated to reflect our repo's HEAD,
// then compares what flux reports for our helloworld deployment versus what we expect.
func TestSync(t *testing.T) {
	h := newharness(t)
	h.applyFlux()
	h.deployViaGit(context.TODO())
	h.verifySyncAndSvcs(t, "HEAD", helloworldImageTag, sidecarImageTag)
}

// TestAutomation does a regular sync, then enables automation and verifies that the
// images get updated in k8s and that commits are pushed to the git repo.  The contents
// of the commits are not verified.
func TestAutomation(t *testing.T) {
	h := newharness(t)
	h.applyFlux()
	h.deployViaGit(context.TODO())
	h.verifySyncAndSvcs(t, "HEAD", helloworldImageTag, sidecarImageTag)

	h.automate()
	ctx, cancel := context.WithTimeout(context.Background(), automationUpdateTimeout)
	h.waitForUpstreamCommits(ctx, 2)
	cancel()

	h.verifySyncAndSvcs(t, "refs/remotes/origin/master", "master-07a1b6b", "master-a000002")
}
