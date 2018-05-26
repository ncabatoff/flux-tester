// require go1.9 for os/user without cgo:

// +build go1.9

package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"log"
	"net/http"
	"os/exec"
	"os/user"
	"testing"
	"time"

	"github.com/weaveworks/flux/api/v6"
	transport "github.com/weaveworks/flux/http"
	"github.com/weaveworks/flux/http/client"
	"github.com/weaveworks/flux/image"
)

func getuser() user.User {
	u, err := user.Current()
	if err != nil {
		log.Fatalf("can't get current user: %v", err)
	}
	return *u
}

func username() string {
	return getuser().Username
}

func homedir() string {
	u := getuser()
	if u.HomeDir == "" {
		log.Fatal("user homedir is empty")
	}
	return u.HomeDir
}

func strOrDie(s string, err error) string {
	if err != nil {
		log.Fatal(err)
	}
	return s
}

func ignoreErr(s string, err error) string {
	if err != nil {
		return ""
	}
	return s
}

func envExec(ctx context.Context, t *testing.T, env []string, command string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, command, args...)
	cmd.Env = env
	if t != nil {
		t.Logf("running %v", cmd.Args)
	} else {
		log.Printf("running %v", cmd.Args)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		err = fmt.Errorf("error running %v: %v\nOutput:\n%s", cmd.Args, err, out)
	}
	return string(out), err
}

func envExecNoErr(ctx context.Context, t *testing.T, env []string, command string, args ...string) string {
	out, err := envExec(ctx, t, env, command, args...)
	return strOrDie(string(out), err)
}

func execNoErr(ctx context.Context, t *testing.T, command string, args ...string) string {
	return envExecNoErr(ctx, t, nil, command, args...)
}

func servicesAPICall(ctx context.Context, namespace string) ([]v6.ControllerStatus, error) {
	api := client.New(http.DefaultClient, transport.NewAPIRouter(), global.svcurl(), "")
	var err error
	var controllers []v6.ControllerStatus
	ticker := time.NewTicker(time.Second)
	for {
		select {
		case <-ticker.C:
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

func httpget(ctx context.Context, url string) (string, error) {
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := ioutil.ReadAll(resp.Body)
	return string(body), err
}

func httpgetNoErr(ctx context.Context, url string) (string, error) {
	var err error
	var body string
	ticker := time.NewTicker(time.Second)
	for {
		select {
		case <-ticker.C:
			body, err = httpget(ctx, url)
			if err == nil {
				return body, nil
			}
		case <-ctx.Done():
			return "", fmt.Errorf("timed out, last error: %v", err)
		}
	}
}
