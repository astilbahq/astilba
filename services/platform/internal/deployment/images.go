package deployment

import (
	"errors"

	"github.com/distribution/reference"
)

func ResolvedImage(source, image string) bool {
	a, err := reference.ParseNormalizedNamed(source)
	if err != nil {
		return false
	}
	b, err := reference.ParseNormalizedNamed(image)
	if err != nil || reference.FamiliarName(a) != reference.FamiliarName(b) {
		return false
	}
	d, ok := b.(reference.Digested)
	if !ok || d.Digest().Algorithm() != "sha256" || d.Digest().Validate() != nil {
		return false
	}
	if expected, ok := a.(reference.Digested); ok && expected.Digest() != d.Digest() {
		return false
	}
	return true
}
func DigestReference(source, digest string) (string, error) {
	a, err := reference.ParseNormalizedNamed(source)
	if err != nil {
		return "", err
	}
	v := reference.FamiliarName(a) + "@" + digest
	if !ResolvedImage(source, v) {
		return "", errors.New("invalid resolved image")
	}
	return v, nil
}
