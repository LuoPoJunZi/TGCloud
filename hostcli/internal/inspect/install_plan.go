package inspect

type InstallInstance struct {
	Directory    string `json:"directory"`
	Present      bool   `json:"present"`
	Version      string `json:"version"`
	Running      bool   `json:"running"`
	GatewayImage string `json:"gateway_image"`
	ConfigDigest string `json:"config_sha256"`
	SourceDigest string `json:"source_sha256"`
}
