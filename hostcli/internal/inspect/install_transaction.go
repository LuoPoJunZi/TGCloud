package inspect

type InstallPlan struct {
	Directory, Source, Config, Action, Version, SourceDigest, OriginalConfigDigest string
}
