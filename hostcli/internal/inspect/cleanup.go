package inspect

const candidateLabel = "com.tgcloud.candidate"

type InstallTemps struct {
	Directory, Stage, Candidate, ConfigPartial, DatabasePartial, SnapshotName, RollbackDatabase string
}
