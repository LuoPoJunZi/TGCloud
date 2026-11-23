package inspect

import (
	"slices"
	"testing"
)

func TestCandidateImagesChecksAllServicePlatformsBeforeImagePreparation(t *testing.T) {
	const base = "example/python:3.12"
	const gateway = "example/clouddrive2:stable"
	dockerfile := []byte("FROM " + base + "\nCOPY app /app\n")
	valid := []byte(`{"services":{"clouddrive2":{"image":"` + gateway + `"},"bot":{"platform":"linux/amd64"}}}`)
	got, err := candidateImages(valid, dockerfile, "linux/amd64")
	if err != nil || !slices.Equal(got, []string{base, gateway}) {
		t.Fatalf("candidate image references: %v, %v", got, err)
	}
	if refs, err := candidateImages(valid, dockerfile, "linux/arm64"); err == nil || refs != nil {
		t.Fatalf("candidate with a mismatched Bot platform returned image references: %v, %v", refs, err)
	}
}
