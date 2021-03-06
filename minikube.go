package test

import (
	"context"
	"fmt"
	"strings"
)

const (
	minikubeProfile = "minikube"
	minikubeVersion = "v0.28.1"
	k8sVersion      = "v1.10.6" // need post-1.9.4 due to https://github.com/kubernetes/kubernetes/issues/61076; need 1.10+ due to https://github.com/kubernetes/minikube/issues/3028.
)

type (
	minikubeTool struct {
		profile string
	}

	minikubeAPI interface {
		version() string
		delete()
		start()
	}

	minikube struct {
		mt minikubeTool
		lg logger
	}

	clusterAPI interface {
		loadDockerImage(string)
		nodeIP() string
	}
)

func (mt minikubeTool) common() []string {
	return []string{"minikube", "--profile", mt.profile}
}

func (mt minikubeTool) versionCmd() []string {
	return append(mt.common(), "version")
}

func (mt minikubeTool) deleteCmd() []string {
	return append(mt.common(), "delete")
}

func (mt minikubeTool) startCmd() []string {
	return append(mt.common(), "start")
}

func (mt minikubeTool) ipCmd() []string {
	return append(mt.common(), "ip")
}

func (mt minikubeTool) dockerEnvCmd() []string {
	return append(mt.common(), "docker-env")
}

func newMinikubeTool(profile string) (*minikubeTool, error) {
	return &minikubeTool{profile: profile}, nil
}

func mustNewMinikube(lg logger, profile string) minikube {
	mt, err := newMinikubeTool(profile)
	if err != nil {
		lg.Fatalf("%v", err)
	}

	m := minikube{mt: *mt, lg: lg}
	version := strings.TrimSpace(m.version())
	if version != fmt.Sprintf("minikube version: %s", minikubeVersion) {
		lg.Fatalf("`minikube version` returned %q, but these tests only support version %s",
			version, minikubeVersion)
	}
	return m
}

func (m minikube) cli() clicmd {
	return newCli(m.lg, nil)
}

func (m minikube) version() string {
	return m.cli().must(context.Background(), m.mt.versionCmd()...)
}

func (m minikube) delete() {
	m.cli().run(context.Background(), m.mt.deleteCmd()...)
}

func (m minikube) start(driver string) {
	var args []string
	if driver != "" {
		args = append(args, []string{"--vm-driver", driver}...)
	}
	m.cli().must(context.Background(), append(m.mt.startCmd(),
		append(args, []string{
			"--bootstrapper", "kubeadm",
			"--keep-context", "--kubernetes-version", k8sVersion}...)...)...)
}

func (m minikube) loadDockerImage(imageName string) {
	shcmd := fmt.Sprintf(`docker save %s | (eval $(%s) && docker load)`, imageName,
		strings.Join(m.mt.dockerEnvCmd(), " "))
	m.cli().must(context.Background(), "sh", "-c", shcmd)
}

func (m minikube) nodeIP() string {
	return strings.TrimSpace(m.cli().must(context.Background(), m.mt.ipCmd()...))
}
