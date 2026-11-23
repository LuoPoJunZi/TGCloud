//go:build !linux

package acceptance

import (
	"io"

	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/hostutil"
	"github.com/LuoPoJunZi/TGCloud/hostcli/internal/inspect"
)

func Supported() bool { return false }

func Run(args []string, input io.Reader, progress io.Writer) (any, error) {
	return nil, hostutil.ErrLinuxRequired
}

func InspectContainerLogRotation(id, directory, service, project, role string, docker inspect.DockerRunner) string {
	return "unknown"
}
