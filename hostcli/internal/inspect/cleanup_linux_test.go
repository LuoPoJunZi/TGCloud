package inspect

import (
	"slices"
	"strings"
	"testing"
)

func TestCleanupOnlyAcceptsArtifactsFromTheInstall(t *testing.T) {
	valid := InstallTemps{
		Directory: "/opt/tgcloud", Stage: "/opt/tgcloud-release-Abcd0123", Candidate: "tgcloud-clouddrive2-candidate:123-456",
		ConfigPartial: "/opt/tgcloud-backups/config-Abcd0123.tar.gz.partial", DatabasePartial: "/opt/tgcloud-backups/database-Abcd0123.db.partial",
		SnapshotName: ".upgrade-backup-456.db", RollbackDatabase: "/opt/tgcloud/data/.rollback-Abcd0123.db",
	}
	if err := validateInstallTemps(valid); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []InstallTemps{
		{Directory: valid.Stage + "/installation", Stage: valid.Stage},
		{Directory: valid.Directory, ConfigPartial: strings.TrimSuffix(valid.ConfigPartial, ".partial")},
		{Directory: valid.Directory, DatabasePartial: strings.TrimSuffix(valid.DatabasePartial, ".partial")},
		{Directory: valid.Directory, RollbackDatabase: valid.Directory + "/data/tgcloud.db"},
		{Directory: valid.Directory, Candidate: valid.Candidate, SnapshotName: ".upgrade-backup-789.db"},
	} {
		if err := validateInstallTemps(bad); err == nil {
			t.Fatalf("accepted a path outside the current installer artifacts: %+v", bad)
		}
	}
}

func TestCleanupCandidateRemovesOnlyLabeledContainerAndExactTag(t *testing.T) {
	const tag = "tgcloud-clouddrive2-candidate:123-456"
	id := strings.Repeat("a", 64)
	responses := []string{id + "\n", "", tag + "\n", ""}
	commands := [][]string{
		{"container", "ls", "--all", "--quiet", "--no-trunc", "--filter", "label=" + candidateLabel + "=" + tag},
		{"container", "rm", "--force", id},
		{"image", "ls", "--filter", "reference=" + tag, "--format", "{{.Repository}}:{{.Tag}}"},
		{"image", "rm", tag},
	}
	call := 0
	docker := func(args ...string) ([]byte, error) {
		if call >= len(commands) || !slices.Equal(args, commands[call]) {
			t.Fatalf("unexpected Docker operation: %q", args)
		}
		result := responses[call]
		call++
		return []byte(result), nil
	}
	if err := cleanupCandidate(tag, docker); err != nil || call != len(commands) {
		t.Fatalf("cleanup calls=%d, error=%v", call, err)
	}
}
