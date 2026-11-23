package storage

import "testing"

func TestInstallerStorageKeepsExistingRecommendationsAndUpgradeBudget(t *testing.T) {
	r := Resources{Architecture: "x86_64", CPU: 2, MemoryTotal: 4 * GiB,
		MemoryAvailable: 3 * GiB, Total: 39 * GiB, Available: 36 * GiB,
		DockerTotal: 39 * GiB, DockerAvailable: 36 * GiB, DockerShared: true, Fuse: true}
	request := Request{Resources: r, RequiresFuse: true}
	value, err := Decide("recommend", request)
	if err != nil {
		t.Fatal(err)
	}
	small := value.(Advice)
	if small.Preferred == nil || *small.Preferred != "stream_first" || small.StreamFirst.Budget != 5 ||
		small.Balanced.Budget != 13 || small.StreamFirst.Reserve != 8 {
		t.Fatalf("39GiB recommendation: %#v", small)
	}

	r.Total, r.Available = 200*GiB, 180*GiB
	request.Resources = r
	value, err = Decide("recommend", request)
	if err != nil {
		t.Fatal(err)
	}
	large := value.(Advice)
	if large.Preferred == nil || *large.Preferred != "balanced" || large.Balanced.Budget != 80 || large.Balanced.Reserve != 40 {
		t.Fatalf("200GiB recommendation: %#v", large)
	}

	r.Installed, r.Available, r.Downloads = true, 13*GiB, 7*GiB
	request = Request{Resources: r, RequiresFuse: true, Budget: 8.5, Reserve: 8.5}
	value, err = Decide("assess", request)
	if err != nil || value != (Assessment{true, 13, "当前配置通过部署前容量校验"}) {
		t.Fatalf("existing occupied downloads or decimal budget changed: %#v, %v", value, err)
	}
	request.Resources.DockerShared = false
	request.Resources.DockerAvailable = 2 * GiB
	value, err = Decide("assess", request)
	if err != nil || value != (Assessment{false, 13, "Docker 数据文件系统至少需要 3GB 可用空间，当前只有 2.0GB"}) {
		t.Fatalf("separate Docker filesystem or upgrade margin changed: %#v, %v", value, err)
	}
}
