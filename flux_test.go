// +build go1.9

// require go1.9 for os/user without cgo
package main

import (
	"context"
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/weaveworks/flux/api/v6"
	transport "github.com/weaveworks/flux/http"
	"github.com/weaveworks/flux/http/client"
	"github.com/weaveworks/flux/image"
)

const (
	minikubeProfile         = "minikube" // until we have a fix for https://github.com/kubernetes/minikube/issues/2717
	minikubeVersion         = "v0.27.0"
	minikubeCommand         = "minikube"
	k8sVersion              = "v1.9.6" // need post-1.9.4 due to https://github.com/kubernetes/kubernetes/issues/61076
	k8sSetupTimeout         = 600 * time.Second
	imageSetupTimeout       = 30 * time.Second
	gitSetupTimeout         = 10 * time.Second
	initialSyncTimeout      = 120 * time.Second
	automationUpdateTimeout = 120 * time.Second
	fluxImage               = "quay.io/weaveworks/flux:latest"
	fluxPort                = "30080"
	gitRepoPathOnNode       = "/home/docker/flux.git"
	fluxNamespace           = "kube-system"
	knownHostsFile          = "ssh-known-hosts"
	gitRepoDir              = "gitrepo"
	secretName              = "flux-git-deploy"
	configMapName           = "ssh-known-hosts"
	helloworldDeploy        = "helloworld-deployment.yaml"
	helloworldDeployTpl     = helloworldDeploy + ".tpl"
	helloworldImageTag      = "master-a000001"
	sidecarImageTag         = "master-a000001"
	appNamespace            = "default"
	fluxDeploy              = "flux-deploy-all.yaml"
	fluxDeployTpl           = fluxDeploy + ".tpl"
	fluxSyncTag             = "flux-sync"
)

var (
	minikubeIP          string
	workdir             string
	sshPrivateKeyPath   = fmt.Sprintf("%s/.minikube/machines/%s/id_rsa", homedir(), minikubeProfile)
	helloworldImageName = image.Name{Domain: "quay.io", Image: "weaveworks/helloworld"}
	sidecarImageName    = image.Name{Domain: "quay.io", Image: "weaveworks/sidecar"}
)

func homedir() string {
	u, err := user.Current()
	if err != nil {
		log.Fatalf("can't get current user: %v", err)
	}
	if u.HomeDir == "" {
		log.Fatal("user homedir is empty")
	}
	return u.HomeDir
}

func execNoErr(ctx context.Context, command string, args ...string) string {
	return envExecNoErr(ctx, nil, command, args...)
}

func envExec(ctx context.Context, env []string, command string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Env = env
	log.Print("running ", command, args)
	out, err := cmd.CombinedOutput()
	if err != nil {
		err = fmt.Errorf("error running %v: %v\nOutput:\n%s", cmd.Args, err, out)
	}
	return string(out), err
}

func envExecNoErr(ctx context.Context, env []string, command string, args ...string) string {
	out, err := envExec(ctx, env, command, args...)
	if err != nil {
		log.Fatal(err)
	}
	return string(out)
}

func verifyMinikube(ctx context.Context) {
	out := execNoErr(ctx, minikubeCommand, "version")
	if out != fmt.Sprintf("minikube version: %s\n", minikubeVersion) {
		log.Fatalf("requires minikube %s, got: %v", minikubeVersion, out)
	}
}

func minikubeSubCmd(subcmd string, args ...string) []string {
	return append([]string{subcmd, "--profile", minikubeProfile}, args...)
}

func minikube(ctx context.Context, subcmd string, args ...string) string {
	allargs := minikubeSubCmd(subcmd, args...)
	return execNoErr(ctx, minikubeCommand, allargs...)
}

func kubectlSubCmd(subcmd string, args ...string) []string {
	return append([]string{"--context", minikubeProfile, "--namespace", fluxNamespace, subcmd}, args...)
}

func kubectl(ctx context.Context, subcmd string, args ...string) string {
	allargs := kubectlSubCmd(subcmd, args...)
	return execNoErr(ctx, "kubectl", allargs...)
}

func kubectlIgnoreErrs(ctx context.Context, subcmd string, args ...string) {
	allargs := kubectlSubCmd(subcmd, args...)
	cmd := exec.CommandContext(ctx, "kubectl", allargs...)
	cmd.Run()
}

func startMinikube(ctx context.Context) {
	verifyMinikube(ctx)
	log.Print(minikube(ctx, "delete"))
	log.Print(minikube(ctx, "start", "--keep-context", "--kubernetes-version", k8sVersion))
}

func loadDockerInMinikube(ctx context.Context, imageName string) {
	shcmd := fmt.Sprintf(`docker save %s | (eval $(%s %s) && docker load)`, imageName,
		minikubeCommand, strings.Join(minikubeSubCmd("docker-env"), " "))
	log.Print(execNoErr(ctx, "sh", "-c", shcmd))
}

func initKubernetes(ctx context.Context) {
	startMinikube(ctx)
}

func initGitRepoOnNode(ctx context.Context) {
	minikube(ctx, "ssh", "--", fmt.Sprintf(
		`set -e; dir="%s"; if [ -d "$dir" ]; then rm -rf "$dir"; fi; git init --bare "$dir"`,
		gitRepoPathOnNode))
}

func gitSubCmd(subcmd string, args ...string) []string {
	gitRepoPath := filepath.Join(workdir, gitRepoDir)
	return append([]string{"-C", gitRepoPath, subcmd}, args...)
}

func git(ctx context.Context, subcmd string, args ...string) (string, error) {
	allargs := gitSubCmd(subcmd, args...)
	env := []string{
		fmt.Sprintf(`GIT_SSH_COMMAND=ssh -i %s -o UserKnownHostsFile=%s`,
			sshPrivateKeyPath, filepath.Join(workdir, knownHostsFile)),
	}
	return envExec(ctx, env, "git", allargs...)
}

func strOrDie(s string, err error) string {
	if err != nil {
		log.Fatal(err)
	}
	return s
}

func gitOrDie(ctx context.Context, subcmd string, args ...string) string {
	return strOrDie(git(ctx, subcmd, args...))
}

func gitIgnoreErr(ctx context.Context, subcmd string, args ...string) string {
	s, err := git(ctx, subcmd, args...)
	if err != nil {
		return ""
	}
	return s
}

func gitURL() string {
	return fmt.Sprintf("ssh://docker@%s%s", minikubeIP, gitRepoPathOnNode)
}

func initGitRepoLocal(ctx context.Context) func() {
	gitRepoPath := filepath.Join(workdir, gitRepoDir)
	execNoErr(ctx, "git", "init", gitRepoPath)
	git(ctx, "remote", "add", "origin", gitURL())
	return func() {
		os.RemoveAll(gitRepoPath)
	}
}

func writeHelloWorldDeployment(repopath string) string {
	tpl, err := template.ParseFiles(helloworldDeployTpl)
	if err != nil {
		log.Fatalf("Unable to parse template %q: %v", helloworldDeployTpl, err)
	}

	foutpath := filepath.Join(repopath, helloworldDeploy)
	fout, err := os.Create(foutpath)
	if err != nil {
		log.Fatalf("Unable to write deployment %q: %v", foutpath, err)
	}

	tpl.ExecuteTemplate(fout, helloworldDeployTpl, struct{ ImageTag string }{helloworldImageTag})

	err = fout.Close()
	if err != nil {
		log.Fatalf("Unable to close deployment %q: %v", foutpath, err)
	}
	return helloworldDeploy
}

func writeFluxDeployment() {
	tpl, err := template.ParseFiles(fluxDeployTpl)
	if err != nil {
		log.Fatalf("Unable to parse template %q: %v", fluxDeployTpl, err)
	}

	foutpath := filepath.Join(workdir, fluxDeploy)
	fout, err := os.Create(foutpath)
	if err != nil {
		log.Fatalf("Unable to write deployment %q: %v", foutpath, err)
	}

	tpl.ExecuteTemplate(fout, fluxDeployTpl, struct {
		FluxImage string
		FluxPort  string
		GitURL    string
	}{fluxImage, fluxPort, gitURL()})

	err = fout.Close()
	if err != nil {
		log.Fatalf("Unable to close deployment %q: %v", foutpath, err)
	}
}

func deployViaGit(ctx context.Context) {
	repopath := filepath.Join(workdir, gitRepoDir)
	gitOrDie(ctx, "add", writeHelloWorldDeployment(repopath))
	gitOrDie(ctx, "commit", "-m", "Deploy helloworld")
	gitOrDie(ctx, "push", "-u", "origin", "master")
}

func waitForSync(ctx context.Context, targetRevSource string) bool {
	headRev := gitOrDie(ctx, "rev-list", "-n", "1", targetRevSource)
	t := time.NewTicker(time.Second)
	for {
		select {
		case <-t.C:
			gitOrDie(ctx, "fetch", "--tags")
			syncRev := gitIgnoreErr(ctx, "rev-list", "-n", "1", fluxSyncTag)
			if syncRev == headRev {
				return true
			}
		case <-ctx.Done():
			log.Fatalf("Failed to sync to revision %s", headRev)
		}

	}
}

func waitForUpstreamCommits(ctx context.Context, mincount int) bool {
	t := time.NewTicker(time.Second)
	for {
		select {
		case <-t.C:
			gitOrDie(ctx, "fetch", "--tags")
			strcount := gitIgnoreErr(ctx, "rev-list", "--count", "HEAD.."+fluxSyncTag)
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
			log.Fatalf("Failed to find at least %d commits", mincount)
		}

	}
}

func svcurl() string {
	u := &url.URL{Scheme: "http", Host: minikubeIP + ":" + fluxPort, Path: "/api/flux"}
	return u.String()
}

func servicesAPICall(ctx context.Context, namespace string) ([]v6.ControllerStatus, error) {
	api := client.New(http.DefaultClient, transport.NewAPIRouter(), svcurl(), "")
	var err error
	var controllers []v6.ControllerStatus
	t := time.NewTicker(time.Second)
	for {
		select {
		case <-t.C:
			controllers, err = api.ListServices(ctx, namespace)
			if err == nil {
				return controllers, nil
			}
		case <-ctx.Done():
			return nil, fmt.Errorf("timed out, last error: %v", err)
		}
	}
}

// services asks flux for the services it's managing, return a map from container name to id.
func services(ctx context.Context, t *testing.T, namespace string, id string) map[string]image.Ref {
	controllers, err := servicesAPICall(ctx, namespace)
	if err != nil {
		t.Errorf("failed to fetch controllers from flux agent: %v", err)
	}

	result := make(map[string]image.Ref)
	for _, controller := range controllers {
		if controller.ID.String() == id {
			for _, c := range controller.Containers {
				result[c.Name] = c.Current.ID
			}
		}
	}

	return result
}

func automate() {
	// In this case, unlike services() we'll invoke fluxctl to enable automation.  From looking at the fluxctl
	// source there's more going on than a simple API call.  And it's not like we have to parse the output.

	execNoErr(context.TODO(), "fluxctl", "--url", svcurl(), "automate",
		fmt.Sprintf("--controller=%s:deployment/helloworld", appNamespace))
}

func TestMain(m *testing.M) {
	var (
		startMinikube = flag.Bool("start-minikube", false, "start minikube (or delete and start if it already exists)")
	)
	flag.Parse()

	if *startMinikube {
		ctx, cancel := context.WithTimeout(context.Background(), k8sSetupTimeout)
		initKubernetes(ctx)
		cancel()
	}

	minikubeIP = strings.TrimSpace(minikube(context.TODO(), "ip"))

	ctx, cancel := context.WithTimeout(context.Background(), imageSetupTimeout)
	loadDockerInMinikube(ctx, fluxImage)
	cancel()

	var err error
	workdir, err = ioutil.TempDir("", "fluxtest")
	if err != nil {
		log.Fatalf("Error creating tempdir: %v", err)
	}
	defer os.RemoveAll(workdir)

	knownHostsContent := execNoErr(context.TODO(), "ssh-keyscan", minikubeIP)
	knownHostsPath := filepath.Join(workdir, knownHostsFile)
	ioutil.WriteFile(knownHostsPath, []byte(knownHostsContent), 0600)

	kubectlIgnoreErrs(context.TODO(), "delete", "secret", secretName)
	kubectl(context.TODO(), "create", "secret", "generic", secretName, "--from-file",
		fmt.Sprintf("identity=%s", sshPrivateKeyPath))

	kubectlIgnoreErrs(context.TODO(), "delete", "configmap", configMapName)
	kubectl(context.TODO(), "create", "configmap", configMapName, "--from-file",
		fmt.Sprintf("known_hosts=%s", knownHostsPath))

	writeFluxDeployment()

	os.Exit(m.Run())
}

func setupFluxAndGitRemote() {
	ctx, cancel := context.WithTimeout(context.Background(), gitSetupTimeout)
	initGitRepoOnNode(ctx)
	cancel()

	kubectlIgnoreErrs(context.TODO(), "delete", "deploy", "flux", "memcached")
	kubectl(context.TODO(), "apply", "-f", filepath.Join(workdir, fluxDeploy))
}

func verifySyncAndSvcs(t *testing.T, targetRevSource, expectedHelloworldTag string, expectedSidecarTag string) {
	ctx, cancel := context.WithTimeout(context.Background(), initialSyncTimeout)
	waitForSync(ctx, targetRevSource)
	got := services(ctx, t, appNamespace, appNamespace+":deployment/helloworld")
	cancel()

	expected := map[string]image.Ref{
		"helloworld": image.Ref{helloworldImageName, expectedHelloworldTag},
		"sidecar":    image.Ref{sidecarImageName, expectedSidecarTag},
	}

	if diff := cmp.Diff(got, expected); diff != "" {
		t.Errorf("Expected %+v, got %+v", expected, got)
	}
}

// TestSync makes sure that the sync tag has been updated to reflect our repo's HEAD,
// then compares what flux reports for our helloworld deployment versus what we expect.
func TestSync(t *testing.T) {
	setupFluxAndGitRemote()
	cleanup := initGitRepoLocal(context.TODO())
	defer cleanup()
	deployViaGit(context.TODO())
	verifySyncAndSvcs(t, "HEAD", helloworldImageTag, sidecarImageTag)
}

// TestAutomation does a regular sync, then enables automation and verifies that the
// images get updated in k8s and that commits are pushed to the git repo.  The contents
// of the commits are not verified.
func TestAutomation(t *testing.T) {
	setupFluxAndGitRemote()
	cleanup := initGitRepoLocal(context.TODO())
	defer cleanup()
	deployViaGit(context.TODO())
	verifySyncAndSvcs(t, "HEAD", helloworldImageTag, sidecarImageTag)

	automate()
	ctx, cancel := context.WithTimeout(context.Background(), automationUpdateTimeout)
	waitForUpstreamCommits(ctx, 2)
	cancel()

	verifySyncAndSvcs(t, "refs/remotes/origin/master", "master-07a1b6b", "master-a000002")
}

// func oldservices() {
// svcurl := url.URL{Scheme: "http",
// 	Host:     minikubeIP + ":" + fluxPort,
// 	Path:     "/api/flux/v6/services",
// 	RawQuery: "namespace=" + namespace}
// req, err := http.NewRequest(http.MethodGet, svcurl.String(), strings.NewReader(""))
// if err != nil {
// 	log.Fatalf("error creating services url: %v", err)
// }

// req = req.WithContext(ctx)
// resp, err := http.DefaultClient.Do(req)
// if err != nil {
// 	log.Fatalf("error fetching services from flux via url %q: %v", svcurl.String(), err)
// }

// cmd := exec.CommandContext(ctx, "jq", "--raw-output",
// 	".[]|select(.ID==\"$1\")|.Containers[]|select(.Name==\"$2\")|.Current.ID")
// cmd.Stdin = resp.Body
// out, err := cmd.CombinedOutput()
// if err != nil {
// 	log.Fatalf("error extracting ")
// }

// TODO is it better to depend on jq or on flux client's dependencies?
// }
