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
	"regexp"
	"strconv"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/weaveworks/flux/image"
)

const (
	minikubeProfile         = "minikube"
	minikubeVersion         = "v0.27.0"
	minikubeCommand         = "minikube"
	k8sVersion              = "v1.9.6" // need post-1.9.4 due to https://github.com/kubernetes/kubernetes/issues/61076
	k8sSetupTimeout         = 600 * time.Second
	imageSetupTimeout       = 30 * time.Second
	gitSetupTimeout         = 10 * time.Second
	syncTimeout             = 120 * time.Second
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

	helmFluxNamespace = "flux"
	helmVersion       = "v2.9.0"
)

type (
	// setup is generally concerned with things that apply globally, and don't
	// depend on harness state.
	setup struct {
		profile   string
		workdir   string
		clusterIP string
	}

	// harness holds state that may be test-specific.
	harness struct {
		setup
		repodir string
		t       *testing.T
	}
)

var (
	helloworldImageName = image.Name{Domain: "quay.io", Image: "weaveworks/helloworld"}
	sidecarImageName    = image.Name{Domain: "quay.io", Image: "weaveworks/sidecar"}
	global              setup
)

func newsetup(profile string) *setup {
	workdir, err := ioutil.TempDir("", "fluxtest")
	if err != nil {
		log.Fatalf("Error creating tempdir: %v", err)
	}

	return &setup{
		workdir: workdir,
		profile: profile,
	}
}

func newharness(t *testing.T) *harness {
	repodir, err := ioutil.TempDir(global.workdir, "git-"+t.Name())
	if err != nil {
		log.Fatalf("Error creating repodir: %v", err)
	}
	return &harness{
		setup:   global,
		repodir: repodir,
		t:       t,
	}
}

func (s *setup) clean() error {
	return os.RemoveAll(s.workdir)
}

func (s *setup) sshPrivateKeyPath() string {
	return fmt.Sprintf("%s/.minikube/machines/%s/id_rsa", homedir(), s.profile)
}

func (s *setup) knownHostsPath() string {
	return filepath.Join(s.workdir, "ssh-known-hosts")
}

func (s *setup) minikubeSubCmd(subcmd string, args ...string) []string {
	return append([]string{subcmd, "--profile", s.profile}, args...)
}

func (s *setup) minikube(ctx context.Context, subcmd string, args ...string) (string, error) {
	allargs := s.minikubeSubCmd(subcmd, args...)
	return envExec(ctx, nil, nil, minikubeCommand, allargs...)
}

func (s *setup) minikubeOrDie(ctx context.Context, subcmd string, args ...string) string {
	return strOrDie(s.minikube(ctx, subcmd, args...))
}

// Make sure we're running a compatible minikube version.
// TODO: validate older versions work provided they use kubeadm/RBAC.
func (s *setup) verifyMinikube(ctx context.Context) {
	out := execNoErr(ctx, nil, minikubeCommand, "version")
	if out != fmt.Sprintf("minikube version: %s\n", minikubeVersion) {
		log.Fatalf("requires minikube %s, got: %v", minikubeVersion, out)
	}
}

// Make sure we're running a compatible kubernetes version.
// TODO: verify RBAC enabled.
func (s *setup) verifyKubernetes(ctx context.Context) {
	out := s.kubectlOrDie(ctx, nil, "", "version")
	re := regexp.MustCompile(`Server Version:.*?GitVersion: *"([^"]+)"`)
	version := re.FindStringSubmatch(out)
	if len(version) != 2 || version[1] != k8sVersion {
		log.Fatalf("requires kubernetes %s, got: %v", k8sVersion, version)
	}
}

// Start minikube.
func (s *setup) startMinikube(ctx context.Context) {
	log.Print(s.minikubeOrDie(ctx, "delete"))
	log.Print(s.minikubeOrDie(ctx, "start",
		"--profile", s.profile,
		"--bootstrapper", "kubeadm",
		"--keep-context", "--kubernetes-version", k8sVersion))
}

// loadDockerInMinikube takes an image available locally and imports it into the cluster.
func (s *setup) loadDockerInMinikube(ctx context.Context, imageName string) {
	shcmd := fmt.Sprintf(`docker save %s | (eval $(%s %s) && docker load)`, imageName,
		minikubeCommand, strings.Join(s.minikubeSubCmd("docker-env"), " "))
	log.Print(execNoErr(ctx, nil, "sh", "-c", shcmd))
}

func (s *setup) kubectlSubCmd(namespace string, subcmd string, args ...string) []string {
	return append([]string{"--context", s.profile, "--namespace", namespace, subcmd}, args...)
}

func (s *setup) kubectl(ctx context.Context, t *testing.T, namespace string, subcmd string, args ...string) (string, error) {
	allargs := s.kubectlSubCmd(namespace, subcmd, args...)
	return envExec(ctx, t, nil, "kubectl", allargs...)
}

func (s *setup) kubectlOrDie(ctx context.Context, t *testing.T, namespace string, subcmd string, args ...string) string {
	return strOrDie(s.kubectl(ctx, t, namespace, subcmd, args...))
}

func (s *setup) kubectlIgnoreErrs(ctx context.Context, t *testing.T, namespace string, subcmd string, args ...string) string {
	return ignoreErr(s.kubectl(ctx, t, namespace, subcmd, args...))
}

func (s *setup) svcurl() string {
	u := &url.URL{Scheme: "http", Host: s.clusterIP + ":" + fluxPort, Path: "/api/flux"}
	return u.String()
}

func (s *setup) helmSubCmd(subcmd string, args ...string) []string {
	return append([]string{"helm",
		"--kube-context", s.profile,
		"--home", filepath.Join(s.workdir, "helm"),
		subcmd}, args...)
}

func (s *setup) helm(ctx context.Context, subcmd string, args ...string) (string, error) {
	allargs := s.helmSubCmd(subcmd, args...)
	return envExec(ctx, nil, nil, allargs[0], allargs[1:]...)
}

func (s *setup) helmOrDie(ctx context.Context, subcmd string, args ...string) string {
	return strOrDie(s.helm(ctx, subcmd, args...))
}

func (s *setup) helmIgnoreErr(ctx context.Context, subcmd string, args ...string) string {
	return ignoreErr(s.helm(ctx, subcmd, args...))
}

func (s *setup) getHelmVersion(ctx context.Context, clientOrServer string) string {
	re := regexp.MustCompile(`Version{SemVer: *"([^"]+)"`)
	helmout := s.helmIgnoreErr(ctx, "version", "--"+clientOrServer, "--tiller-connection-timeout", "5")
	version := re.FindStringSubmatch(helmout)
	if len(version) == 2 {
		return version[1]
	}
	return ""
}

func (s *setup) initHelm(ctx context.Context) {
	s.kubectlOrDie(ctx, nil, "kube-system", "create", "sa", "tiller")
	s.kubectlOrDie(ctx, nil, "kube-system", "create", "clusterrolebinding", "tiller-cluster-rule",
		"--clusterrole=cluster-admin", "--serviceaccount=kube-system:tiller")
	s.helmOrDie(ctx, "init", "--wait", "--skip-refresh", "--upgrade", "--service-account", "tiller")
}

func (s *setup) run() {
	s.clusterIP = strings.TrimSpace(s.minikubeOrDie(context.TODO(), "ip"))

	s.verifyKubernetes(context.Background())

	if helmverser := global.getHelmVersion(context.Background(), "server"); helmverser == "" {
		s.initHelm(context.TODO())
	} else if helmverser != helmVersion {
		log.Fatalf("requires helm server version %s, got: %v", helmVersion, helmverser)
	}

	ctx, cancel := context.WithTimeout(context.Background(), imageSetupTimeout)
	s.loadDockerInMinikube(ctx, fluxImage)
	s.loadDockerInMinikube(ctx, fluxOperatorImage)
	cancel()

	knownHostsContent := execNoErr(context.TODO(), nil, "ssh-keyscan", s.clusterIP)
	ioutil.WriteFile(s.knownHostsPath(), []byte(knownHostsContent), 0600)

	secretName := "flux-git-deploy"
	s.kubectlIgnoreErrs(context.TODO(), nil, fluxNamespace, "delete", "secret", secretName)
	s.kubectlOrDie(context.TODO(), nil, fluxNamespace, "create", "secret", "generic", secretName, "--from-file",
		fmt.Sprintf("identity=%s", s.sshPrivateKeyPath()))

	configMapName := "ssh-known-hosts"
	s.kubectlIgnoreErrs(context.TODO(), nil, fluxNamespace, "delete", "configmap", configMapName)
	s.kubectlOrDie(context.TODO(), nil, fluxNamespace, "create", "configmap", configMapName, "--from-file",
		fmt.Sprintf("known_hosts=%s", s.knownHostsPath()))
}

func (h *harness) initGitRepoOnNode(ctx context.Context) {
	h.minikubeOrDie(ctx, "ssh", "--", fmt.Sprintf(
		`set -e; dir="%s"; if [ -d "$dir" ]; then rm -rf "$dir"; fi; git init --bare "$dir"`,
		gitRepoPathOnNode))
}

func (h *harness) setupGitRemote() {
	ctx, cancel := context.WithTimeout(context.Background(), gitSetupTimeout)
	h.initGitRepoOnNode(ctx)
	cancel()
}

func (h *harness) gitSubCmd(subcmd string, args ...string) []string {
	return append([]string{"-C", h.repodir, subcmd}, args...)
}

func (h *harness) git(ctx context.Context, subcmd string, args ...string) (string, error) {
	allargs := h.gitSubCmd(subcmd, args...)
	env := []string{
		fmt.Sprintf(`GIT_SSH_COMMAND=ssh -i %s -o UserKnownHostsFile=%s`,
			h.sshPrivateKeyPath(), h.knownHostsPath()),
	}
	return envExec(ctx, h.t, env, "git", allargs...)
}

func (h *harness) gitOrDie(ctx context.Context, subcmd string, args ...string) string {
	return strOrDie(h.git(ctx, subcmd, args...))
}

func (h *harness) gitIgnoreErr(ctx context.Context, subcmd string, args ...string) string {
	return ignoreErr(h.git(ctx, subcmd, args...))
}

func (h *harness) gitURL() string {
	return fmt.Sprintf("ssh://docker@%s%s", h.clusterIP, gitRepoPathOnNode)
}

func (h *harness) initGitRepoLocal(ctx context.Context) {
	execNoErr(ctx, h.t, "git", "init", h.repodir)
	h.git(ctx, "remote", "add", "origin", h.gitURL())
}

func (h *harness) writeHelloWorldDeployment() string {
	helloworldDeploy := "helloworld-deployment.yaml"
	helloworldDeployTpl := helloworldDeploy + ".tpl"
	helloworldDeployTplPath := filepath.Join("nohelm", helloworldDeployTpl)

	tpl, err := template.ParseFiles(helloworldDeployTplPath)
	if err != nil {
		h.t.Fatalf("Unable to parse template %q: %v", helloworldDeployTplPath, err)
	}

	foutpath := filepath.Join(h.repodir, helloworldDeploy)
	fout, err := os.Create(foutpath)
	if err != nil {
		h.t.Fatalf("Unable to write deployment %q: %v", foutpath, err)
	}

	tpl.ExecuteTemplate(fout, helloworldDeployTpl, struct{ ImageTag string }{helloworldImageTag})

	err = fout.Close()
	if err != nil {
		h.t.Fatalf("Unable to close deployment %q: %v", foutpath, err)
	}
	return helloworldDeploy
}

func (h *harness) writeFluxDeployment() string {
	fluxDeploy := "flux-deploy-all.yaml"
	fluxDeployTpl := fluxDeploy + ".tpl"
	fluxDeployTplPath := filepath.Join("nohelm", fluxDeployTpl)

	tpl, err := template.ParseFiles(fluxDeployTplPath)
	if err != nil {
		h.t.Fatalf("Unable to parse template %q: %v", fluxDeployTplPath, err)
	}

	foutpath := filepath.Join(h.workdir, fluxDeploy)
	fout, err := os.Create(foutpath)
	if err != nil {
		h.t.Fatalf("Unable to write deployment %q: %v", foutpath, err)
	}

	tpl.ExecuteTemplate(fout, fluxDeployTpl, struct {
		FluxImage string
		FluxPort  string
		GitURL    string
	}{fluxImage, fluxPort, h.gitURL()})

	err = fout.Close()
	if err != nil {
		h.t.Fatalf("Unable to close deployment %q: %v", foutpath, err)
	}
	return foutpath
}

func (h *harness) deployViaGit(ctx context.Context) {
	h.gitOrDie(ctx, "add", h.writeHelloWorldDeployment())
	h.gitOrDie(ctx, "commit", "-m", "Deploy helloworld")
	h.gitOrDie(ctx, "push", "-u", "origin", "master")
}

func (h *harness) waitForSync(ctx context.Context, targetRevSource string) bool {
	headRev := h.gitOrDie(ctx, "rev-list", "-n", "1", targetRevSource)
	t := time.NewTicker(time.Second)
	for {
		select {
		case <-t.C:
			h.gitOrDie(ctx, "fetch", "--tags")
			syncRev := h.gitIgnoreErr(ctx, "rev-list", "-n", "1", fluxSyncTag)
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
			h.gitOrDie(ctx, "fetch", "--tags")
			strcount := h.gitIgnoreErr(ctx, "rev-list", "--count", "HEAD.."+fluxSyncTag)
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

	execNoErr(context.TODO(), h.t, "fluxctl", "--url", h.svcurl(), "automate",
		fmt.Sprintf("--controller=%s:deployment/helloworld", appNamespace))
}

func (h *harness) applyFlux() {
	h.kubectlIgnoreErrs(context.TODO(), h.t, fluxNamespace, "delete", "deploy", "flux", "memcached")
	h.kubectlOrDie(context.TODO(), h.t, fluxNamespace, "apply", "-f", h.writeFluxDeployment())
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
		got = services(ctx, t, appNamespace, appNamespace+":deployment/helloworld")
		diff = cmp.Diff(got, expected)
	}
	cancel()

	if diff != "" {
		t.Errorf("Expected %+v, got %+v, diff: %s", expected, got, diff)
	}
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

	global = *newsetup(*flagMinikubeProfile)
	if !*flagKeepWorkdir {
		defer global.clean()
	}

	global.verifyMinikube(context.Background())
	if helmvercli := global.getHelmVersion(context.Background(), "client"); helmvercli != helmVersion {
		log.Fatalf("requires helm client version %s, got: %v", helmVersion, helmvercli)
	}

	if *flagStartMinikube {
		ctx, cancel := context.WithTimeout(context.Background(), k8sSetupTimeout)
		global.startMinikube(ctx)
		cancel()
	}

	global.run()
	os.Exit(m.Run())
}

// TestSync makes sure that the sync tag has been updated to reflect our repo's HEAD,
// then compares what flux reports for our helloworld deployment versus what we expect.
func TestSync(t *testing.T) {
	h := newharness(t)
	h.setupGitRemote()
	h.applyFlux()
	h.initGitRepoLocal(context.TODO())
	h.deployViaGit(context.TODO())
	h.verifySyncAndSvcs(t, "HEAD", helloworldImageTag, sidecarImageTag)
}

// TestAutomation does a regular sync, then enables automation and verifies that the
// images get updated in k8s and that commits are pushed to the git repo.  The contents
// of the commits are not verified.
func TestAutomation(t *testing.T) {
	h := newharness(t)
	h.setupGitRemote()
	h.applyFlux()
	h.initGitRepoLocal(context.TODO())
	h.deployViaGit(context.TODO())
	h.verifySyncAndSvcs(t, "HEAD", helloworldImageTag, sidecarImageTag)

	h.automate()
	ctx, cancel := context.WithTimeout(context.Background(), automationUpdateTimeout)
	h.waitForUpstreamCommits(ctx, 2)
	cancel()

	h.verifySyncAndSvcs(t, "refs/remotes/origin/master", "master-07a1b6b", "master-a000002")
}
