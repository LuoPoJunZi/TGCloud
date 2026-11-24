//go:build !linux

package inspect

import (
	"errors"
	"io"
	"os/exec"
)

var errLinuxRequired = errors.New("此操作需要 Linux 宿主机")

func LinuxSupported() bool { return false }

func LockInstance() (io.Closer, error) {
	return nil, errLinuxRequired
}

func readLogConfig(directory string) ([]byte, error) {
	return nil, errLinuxRequired
}

func verifyCandidateStage(directory string) error {
	return errLinuxRequired
}

func BackupUpgradeConfig(directory, destination string) error {
	return errLinuxRequired
}

func SnapshotUpgradeDatabase(directory, tag, name, destination string) error {
	return errLinuxRequired
}

func CommitUpgradeBackup(directory, pending string) error {
	return errLinuxRequired
}

func QuiesceUpgradeBot(directory, stage string, output io.Writer) error {
	return errLinuxRequired
}

func ResumeUpgradeBot(directory, stage string, output io.Writer) error {
	return errLinuxRequired
}

func ApplyUpgradeFiles(directory, stage string, applyConfig bool) error {
	return errLinuxRequired
}

func RollbackUpgrade(directory, archive, database, oldImage, oldTag string) error {
	return errLinuxRequired
}

func DeployBot(directory string, progress io.Writer) error {
	return errLinuxRequired
}

func DeployBotForInstall(directory, candidate string, progress io.Writer) error {
	return errLinuxRequired
}

func RunInstallTransaction(plan InstallPlan, output io.Writer) error {
	return errLinuxRequired
}

func DiscoverInstall(directory string) (InstallInstance, error) {
	return InstallInstance{}, errLinuxRequired
}

func VerifyInstallPlan(expected InstallInstance, source, targetVersion, targetDigest string) (string, error) {
	return "", errLinuxRequired
}

func InstallerPreflightSupported() bool { return false }

func InstallerPreflight(args []string, input io.Reader) (any, error) {
	return nil, errLinuxRequired
}

func ManagerRuntimeSupported() bool { return false }

func ManagerRuntime(args []string, input io.Reader) (any, error) {
	return nil, errLinuxRequired
}

func InstallSourceSupported() bool { return false }

func InstallSource(args []string, input io.Reader) (any, error) {
	return nil, errLinuxRequired
}

func isolateInstallCommand(command *exec.Cmd, killGroup bool) {}

func AcceptBot(directory string, checkGateway bool, progress io.Writer) error {
	return errLinuxRequired
}

func StartGateway(directory string, progress io.Writer) error {
	return errLinuxRequired
}

func RepairGatewayNetwork(directory, stage string, progress io.Writer) error {
	return errLinuxRequired
}

func RestoreGatewayNetwork(directory, stage string) error {
	return errLinuxRequired
}

func CleanupInstall(temps InstallTemps) error {
	return errLinuxRequired
}

func CheckInstallResources(directory string, progress io.Writer) error {
	return errLinuxRequired
}

func CheckInstallHost(directory, config string, progress io.Writer) error {
	return errLinuxRequired
}

func PrepareInstallLayout(directory, stage, config string) error {
	return errLinuxRequired
}

func StageFreshInstall(directory, source, config string, result io.Writer) error {
	return errLinuxRequired
}

func StageUpgrade(directory, source string, result io.Writer) error {
	return errLinuxRequired
}

func PrepareInstallConfig(directory, config string, result io.Writer) error {
	return errLinuxRequired
}

func PreflightCandidate(stage string) error {
	return errLinuxRequired
}

func CheckInstallCompose(directory string, result io.Writer) error {
	return errLinuxRequired
}

func PublishFreshInstall(directory, stage string) error {
	return errLinuxRequired
}

func FinalizeInstallLayout(directory, stage string) error {
	return errLinuxRequired
}

func ResourceProbe(install, backup string) (string, error) {
	return "", errLinuxRequired
}
