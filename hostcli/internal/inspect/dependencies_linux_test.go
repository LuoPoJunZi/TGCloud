package inspect

import (
	"errors"
	"io"
	"slices"
	"strings"
	"testing"
)

func TestPrepareDependenciesReusesRunningDocker(t *testing.T) {
	for _, upgrade := range []bool{false, true} {
		composeReady := upgrade
		run := func(name string, args ...string) ([]byte, error) {
			switch name {
			case "docker":
				if composeReady {
					return nil, nil
				}
				return []byte("docker: unknown command: docker compose\n"), errors.New("missing plugin")
			case "dpkg-query":
				pkg := args[len(args)-1]
				if strings.HasPrefix(pkg, "docker-compose-") {
					return []byte("dpkg-query: no packages found matching " + pkg), errors.New("missing package")
				}
				return []byte("installed"), nil
			case "apt-get":
				if !upgrade && slices.Contains(args, "install") && args[len(args)-1] == "docker-compose-plugin" {
					if args[0] == "-s" {
						return []byte("Inst docker-compose-plugin (1.0 stable [amd64])\nConf docker-compose-plugin (1.0 stable [amd64])\n"), nil
					}
					composeReady = true
					return nil, nil
				}
			}
			t.Fatalf("unexpected command for upgrade=%v: %s %v", upgrade, name, args)
			return nil, nil
		}
		err := prepareDependencies(upgrade, true, io.Discard, run, func() error { return nil }, func() error {
			if upgrade {
				t.Fatal("upgrade with working Compose should reuse the existing Docker repository")
			}
			return nil
		})
		if err != nil || !composeReady {
			t.Fatalf("upgrade=%v: Compose ready=%v, error=%v", upgrade, composeReady, err)
		}
	}
}

func TestInstallDependencyPackagesRejectsAPTChanges(t *testing.T) {
	for _, plan := range []string{
		"Inst docker-ce [28.0] (29.0 stable [amd64])\n",
		"Remv docker.io [28.0]\n",
		"Conf docker-ce (29.0 stable [amd64])\n",
	} {
		run := func(name string, args ...string) ([]byte, error) {
			if name != "apt-get" || args[0] != "-s" {
				t.Fatalf("unexpected mutation for plan %q: %s %v", plan, name, args)
			}
			return []byte(plan), nil
		}
		if err := installDependencyPackages([]string{"docker-compose-plugin"}, run); err == nil {
			t.Fatalf("accepted APT plan %q", plan)
		}
	}
}
