package inspect

import "testing"

func TestInstallerStorageKeepsExistingRecommendationsAndUpgradeBudget(t *testing.T) {
	r := storageResources{Architecture: "x86_64", CPU: 2, MemoryTotal: 4 * storageGiB,
		MemoryAvailable: 3 * storageGiB, Total: 39 * storageGiB, Available: 36 * storageGiB,
		DockerTotal: 39 * storageGiB, DockerAvailable: 36 * storageGiB, DockerShared: true, Fuse: true}
	request := storageRequest{Resources: r, RequiresFuse: true}
	value, err := decideStorage("recommend", request)
	if err != nil {
		t.Fatal(err)
	}
	small := value.(storageAdvice)
	if small.Preferred == nil || *small.Preferred != "stream_first" || small.StreamFirst.Budget != 5 ||
		small.Balanced.Budget != 13 || small.StreamFirst.Reserve != 8 {
		t.Fatalf("39GiB recommendation: %#v", small)
	}

	r.Total, r.Available = 200*storageGiB, 180*storageGiB
	request.Resources = r
	value, err = decideStorage("recommend", request)
	if err != nil {
		t.Fatal(err)
	}
	large := value.(storageAdvice)
	if large.Preferred == nil || *large.Preferred != "balanced" || large.Balanced.Budget != 80 || large.Balanced.Reserve != 40 {
		t.Fatalf("200GiB recommendation: %#v", large)
	}

	r.Installed, r.Available, r.Downloads = true, 13*storageGiB, 7*storageGiB
	request = storageRequest{Resources: r, RequiresFuse: true, Budget: 8.5, Reserve: 8.5}
	value, err = decideStorage("assess", request)
	if err != nil || value != (storageAssessment{true, 13, "当前配置通过部署前容量校验"}) {
		t.Fatalf("existing occupied downloads or decimal budget changed: %#v, %v", value, err)
	}
	request.Resources.DockerShared = false
	request.Resources.DockerAvailable = 2 * storageGiB
	value, err = decideStorage("assess", request)
	if err != nil || value != (storageAssessment{false, 13, "Docker 数据文件系统至少需要 3GB 可用空间，当前只有 2.0GB"}) {
		t.Fatalf("separate Docker filesystem or upgrade margin changed: %#v, %v", value, err)
	}
}
