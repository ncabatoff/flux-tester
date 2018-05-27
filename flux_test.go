// +build go1.9

// require go1.9 for os/user without cgo
package main

import (
	"context"
	"flag"
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
	syncTimeout       = 20 * time.Second
	// releaseTimeout is how long we allow between seeing sync done and seeing
	// a change made to a helm release.
	releaseTimeout          = 10 * time.Second
	automationUpdateTimeout = 180 * time.Second
	fluxImage               = "quay.io/weaveworks/flux:latest"
	fluxOperatorImage       = "quay.io/weaveworks/helm-operator:latest"
	fluxPort                = "30080"
	gitRepoPathOnNode       = "/home/docker/flux.git"
	fluxNamespace           = "flux"
	helloworldImageTag      = "master-a000001"
	sidecarImageTag         = "master-a000001"
	appNamespace            = "default"
	fluxSyncTag             = "flux-sync"
	helmFluxRelease         = "cd"
)

type (
	// setup is generally concerned with things that apply globally, and don't
	// depend on harness state.
	workdir struct {
		root          string
		sshKnownHosts string
	}

	setup struct {
		workdir
		profile   string
		clusterIP string
		clusterAPI
		kubectlAPI
		helmAPI
	}

	// harness holds state that may be test-specific.
	harness struct {
		workdir
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
	global              setup
)

func newsetup(profile string) *setup {
	dir, err := ioutil.TempDir("", "fluxtest")
	if err != nil {
		log.Fatalf("Error creating tempdir: %v", err)
	}

	return &setup{
		workdir: workdir{root: dir},
		profile: profile,
	}
}

func newharness(t *testing.T) *harness {
	testdir := filepath.Join(global.workdir.root, t.Name())
	os.Mkdir(testdir, 0755)
	repodir := filepath.Join(testdir, "repo")

	h := &harness{
		workdir:    global.workdir,
		repodir:    repodir,
		t:          t,
		clusterIP:  global.clusterIP,
		clusterAPI: minikube{mt: global.clusterAPI.(minikube).mt, lg: t},
		helmAPI:    helm{ht: global.helmAPI.(helm).ht, lg: t},
	}

	ctx, cancel := context.WithTimeout(context.Background(), gitSetupTimeout)
	h.initGitRepoOnNode(ctx)
	cancel()

	h.gitAPI = mustNewGit(t, repodir,
		fmt.Sprintf(`ssh -i %s -o UserKnownHostsFile=%s`, h.sshKeyPath(), h.knownHostsPath()),
		h.gitURL())
	return h
}

func (s *setup) clean() error {
	return os.RemoveAll(s.workdir.root)
}

func (w workdir) knownHostsPath() string {
	return filepath.Join(w.root, "ssh-known-hosts")
}

func (s *setup) must(err error) {
	if err != nil {
		log.Fatalf("%s", err)
	}
}

func (s *setup) ssh(namespace string) {
	knownHostsContent := execNoErr(context.TODO(), nil, "ssh-keyscan", s.clusterIP)
	ioutil.WriteFile(s.knownHostsPath(), []byte(knownHostsContent), 0600)

	s.kubectlAPI.create("", "namespace", namespace)

	secretName := "flux-git-deploy"
	s.kubectlAPI.delete(namespace, "secret", secretName)
	s.must(s.kubectlAPI.create(namespace, "secret", "generic", secretName, "--from-file",
		fmt.Sprintf("identity=%s", s.sshKeyPath())))

	configMapName := "ssh-known-hosts"
	s.kubectlAPI.delete(namespace, "configmap", configMapName)
	s.must(s.kubectlAPI.create(namespace, "configmap", configMapName, "--from-file",
		fmt.Sprintf("known_hosts=%s", s.knownHostsPath())))
}

func (h *harness) initGitRepoOnNode(ctx context.Context) {
	h.clusterAPI.sshToNode(fmt.Sprintf(
		`set -e; dir="%s"; if [ -d "$dir" ]; then rm -rf "$dir"; fi; git init --bare "$dir"`,
		gitRepoPathOnNode))
}

func (h *harness) gitURL() string {
	return fmt.Sprintf("ssh://docker@%s%s", h.clusterIP, gitRepoPathOnNode)
}

func (h *harness) fluxURL() string {
	u := &url.URL{Scheme: "http", Host: h.clusterIP + ":" + fluxPort, Path: "/api/flux"}
	return u.String()
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

func writeFluxDeployment(destdir string, giturl string) (string, error) {
	return writeTemplate(destdir, "nohelm/flux-deploy-all.yaml.tpl",
		struct {
			FluxImage string
			FluxPort  string
			GitURL    string
		}{fluxImage, fluxPort, giturl})
}

func writeNamespace(destdir string) (string, error) {
	return writeTemplate(destdir, "namespace.yaml.tpl",
		struct{ Namespace string }{fluxNamespace})
}

func (h *harness) deployViaGit(ctx context.Context) {
	_, err := writeHelloWorldDeployment(h.repodir)
	if err != nil {
		h.t.Fatal(err)
	}
	h.mustAddCommitPush()
}

func (h *harness) waitForSync(ctx context.Context, targetRevSource string) bool {
	headRev, err := h.revlist("-n", "1", targetRevSource)
	if err != nil {
		h.t.Fatalf("Unable to get head rev: %v", err)
	}
	t := time.NewTicker(time.Second)
	for {
		select {
		case <-t.C:
			h.mustFetch()
			syncRev, _ := h.revlist("-n", "1", fluxSyncTag)
			if syncRev == headRev {
				return true
			}
		case <-ctx.Done():
			h.t.Fatalf("Failed to sync to revision %s", headRev)
		}

	}
}

func (h *harness) waitForUpstreamCommits(ctx context.Context, mincount int) bool {
	ticker := time.NewTicker(time.Second)
	for {
		select {
		case <-ticker.C:
			h.mustFetch()
			strcount, _ := h.revlist("--count", "HEAD.."+fluxSyncTag)
			if strcount != "" {
				count, err := strconv.Atoi(strings.TrimSpace(strcount))
				if err != nil {
					log.Fatalf("git rev-list --count returned a non-numeric output %q: %v", strcount, err)
				}
				if count >= mincount {
					return true
				}
			}
		case <-ctx.Done():
			h.t.Fatalf("Failed to find at least %d commits", mincount)
		}

	}
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

	ctx, cancel := context.WithTimeout(context.Background(), syncTimeout)
	h.waitForSync(ctx, targetRevSource)
	for got == nil || diff != "" {
		got = h.services(ctx, t, appNamespace, appNamespace+":deployment/helloworld")
		diff = cmp.Diff(got, expected)
	}
	cancel()

	if diff != "" {
		t.Errorf("Expected %+v, got %+v, diff: %s", expected, got, diff)
	}
}

func setupPath() {
	cwd, err := os.Getwd()
	if err != nil {
		log.Fatalf("cannot get working directory: %v", err)
	}
	envpath := os.Getenv("PATH")
	if envpath == "" {
		envpath = filepath.Join(cwd, "bin")
	} else {
		envpath = filepath.Join(cwd, "bin") + ":" + envpath
	}
	os.Setenv("PATH", envpath)
}

func TestMain(m *testing.M) {
	var (
		flagKeepWorkdir = flag.Bool("keep-workdir", false,
			"don't delete workdir on exit")
		flagStartMinikube = flag.Bool("start-minikube", false,
			"start minikube (or delete and start if it already exists)")
		flagMinikubeProfile = flag.String("minikube-profile", "minikube",
			"minikube profile to use, don't change until we have a fix for https://github.com/kubernetes/minikube/issues/2717")
	)
	flag.Parse()
	log.Printf("Testing with keep-workdir=%v, start-minikube=%v, minikube-profile=%v",
		*flagKeepWorkdir, *flagStartMinikube, *flagMinikubeProfile)

	setupPath()

	global = *newsetup(*flagMinikubeProfile)
	if !*flagKeepWorkdir {
		defer global.clean()
	}

	minikube := mustNewMinikube(stdLogger{}, *flagMinikubeProfile)
	if *flagStartMinikube {
		minikube.delete()
		minikube.start()
	}

	global.clusterAPI = minikube
	global.clusterIP = minikube.nodeIP()
	global.kubectlAPI = mustNewKubectl(stdLogger{}, *flagMinikubeProfile)
	global.helmAPI = mustNewHelm(stdLogger{}, *flagMinikubeProfile,
		global.workdir.root, global.kubectlAPI)

	global.loadDockerImage(fluxImage)
	global.loadDockerImage(fluxOperatorImage)

	global.ssh(fluxNamespace)

	// Make sure that if helm flux is sitting around due to a previous failed
	// test, it won't interfere with upcoming tests.
	global.helmAPI.delete(helmFluxRelease, true)

	os.Exit(m.Run())
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
