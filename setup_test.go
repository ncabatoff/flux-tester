// +build integration_test

package test

import (
	"context"
	"flag"
	"io/ioutil"
	"log"
	"os"
	"path/filepath"
	"testing"
	"time"
)

const (
	fluxImage         = "quay.io/weaveworks/flux:latest"
	fluxOperatorImage = "quay.io/weaveworks/helm-operator:latest"
	fluxNamespace     = "flux"
	helmFluxRelease   = "cd"
	helmGitRelease    = "git"
)

type (
	// setup is generally concerned with things that apply globally, and don't
	// depend on harness state.
	setup struct {
		testroot  string
		profile   string
		clusterIP string
		clusterAPI
		kubectlAPI
		helmAPI
	}
)

var (
	// global is our one global variable, containing the setup common
	// to all tests.
	global *setup
)

func newsetup(profile string) *setup {
	dir, err := ioutil.TempDir("", "fluxtest")
	if err != nil {
		log.Fatalf("Error creating tempdir: %v", err)
	}

	return &setup{
		testroot: dir,
		profile:  profile,
	}
}

func (s *setup) clean() error {
	return os.RemoveAll(s.testroot)
}

func (s *setup) genSshPrivateKey() {
	// Create ssh dir under workdir and generate ssh key
	_ = os.Mkdir(s.sshDir(), 0700)
	// pubkey := privkey + ".pub"
	execNoErr(context.Background(), nil, "ssh-keygen", "-t", "rsa", "-N", "", "-f", s.sshKeyFilePrivate())
}

func (s *setup) sshDir() string {
	return filepath.Join(s.testroot, "ssh")
}

func (s *setup) sshKeyFilePrivate() string {
	return filepath.Join(s.sshDir(), "id_rsa")
}

func (s *setup) sshKeyFilePublic() string {
	return s.sshKeyFilePrivate() + ".pub"
}

func (s *setup) knownHostsPath() string {
	return filepath.Join(s.sshDir(), "ssh-known-hosts")
}

func (s *setup) must(err error) {
	if err != nil {
		log.Fatalf("%s", err)
	}
}

func setEnvPath() {
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
		flagMinikubeDriver = flag.String("minikube-driver", "",
			"minikube driver to use")
		flagMinikubeProfile = flag.String("minikube-profile", "minikube",
			"minikube profile to use, don't change until we have a fix for https://github.com/kubernetes/minikube/issues/2717")
	)
	flag.Parse()
	log.Printf("Testing with keep-workdir=%v, start-minikube=%v, minikube-driver=%v, minikube-profile=%v",
		*flagKeepWorkdir, *flagStartMinikube, *flagMinikubeDriver, *flagMinikubeProfile)

	setEnvPath()

	global = newsetup(*flagMinikubeProfile)
	if !*flagKeepWorkdir {
		defer global.clean()
	}

	global.genSshPrivateKey()

	minikube := mustNewMinikube(stdLogger{}, *flagMinikubeProfile)
	if *flagStartMinikube {
		minikube.delete()
		minikube.start(*flagMinikubeDriver)
		// This sleep is a hack until we find a better way to determine
		// when the cluster is stable.
		time.Sleep(60 * time.Second)
	}

	global.clusterAPI = minikube
	global.clusterIP = minikube.nodeIP()
	global.kubectlAPI = mustNewKubectl(stdLogger{}, *flagMinikubeProfile)
	global.helmAPI = mustNewHelm(stdLogger{}, *flagMinikubeProfile,
		global.testroot, global.kubectlAPI)

	if *flagMinikubeDriver != "none" {
		global.loadDockerImage(fluxImage)
		global.loadDockerImage(fluxOperatorImage)
	}

	global.kubectlAPI.create("", "namespace", fluxNamespace)

	// Make sure that if helm flux is sitting around due to a previous failed
	// test, it won't interfere with upcoming tests.
	global.helmAPI.delete(helmFluxRelease, true)

	os.Exit(m.Run())
}
