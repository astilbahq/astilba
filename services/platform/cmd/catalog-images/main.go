// Command catalog-images prints, one per line, each image the service catalog deploys, leaving
// out the images given as arguments. The Host visual lane passes the fixture images it has
// already pulled and pulls the rest before its catalog journey, so a catalog pin lives only in
// the catalog.
package main

import (
	"fmt"
	"io"
	"os"

	"github.com/astilbahq/astilba/services/platform/internal/catalog"
	"github.com/distribution/reference"
)

func main() {
	if err := run(os.Stdout, catalog.Images(), os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "catalog-images:", err)
		os.Exit(1)
	}
}

func run(w io.Writer, images, pulled []string) error {
	skip := map[string]bool{}
	for _, image := range pulled {
		if err := pinned(image); err != nil {
			return err
		}
		skip[image] = true
	}
	for _, image := range images {
		if err := pinned(image); err != nil {
			return fmt.Errorf("catalog %w", err)
		}
	}
	for _, image := range images {
		if !skip[image] {
			if _, err := fmt.Fprintln(w, image); err != nil {
				return err
			}
		}
	}
	return nil
}

// pinned refuses a reference without both a tag, which the pin report reads, and a SHA-256
// digest, which the pull checks the bytes against.
func pinned(image string) error {
	named, err := reference.ParseNormalizedNamed(image)
	if err != nil {
		return fmt.Errorf("image %q: %w", image, err)
	}
	_, tagged := named.(reference.Tagged)
	digested, ok := named.(reference.Digested)
	if !tagged || !ok || digested.Digest().Algorithm() != "sha256" || reference.FamiliarString(named) != image {
		return fmt.Errorf("image %q needs a tag and a sha256 digest, written as Docker shows it", image)
	}
	return nil
}
