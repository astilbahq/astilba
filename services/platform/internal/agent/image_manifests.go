package agent

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"path"
	"strings"
)

// imageChainsCost counts the layers Docker unpacks for every image the archive's manifests
// describe. Docker's legacy store reads manifest.json and the containerd store also index.json,
// so both count. Docker unpacks a layer once for each distinct chain of layers below and
// including it, so a layer listed twice, or below different layers, counts each time, as does
// each layer's own bookkeeping.
func imageChainsCost(members map[string]*imageMember, limit imageLoadLimit) (imageCost, error) {
	var chains [][]string
	if m := members["manifest.json"]; m != nil {
		var images []jsontext.Value
		if m.data == nil || json.Unmarshal(m.data, &images) != nil {
			return imageCost{}, unmeasurableImage("its manifest.json is not valid")
		}
		for _, image := range images {
			lists, err := foldedMembers(image, "Layers")
			if err != nil {
				return imageCost{}, err
			}
			for _, list := range lists {
				var layers []string
				if json.Unmarshal(list, &layers) != nil {
					return imageCost{}, unmeasurableImage("its manifest.json is not valid")
				}
				chain := make([]string, len(layers))
				for i, layer := range layers {
					// Docker's legacy store resolves each path inside the archive.
					chain[i] = strings.TrimPrefix(path.Clean("/"+layer), "/")
				}
				chains = append(chains, chain)
			}
		}
	}
	if members["index.json"] != nil {
		visited := map[string]bool{}
		if err := indexChains(members, "index.json", 0, visited, &chains); err != nil {
			return imageCost{}, err
		}
	}
	type node struct {
		parent int32
		name   string
	}
	nodes := map[node]int32{}
	var cost imageCost
	for _, chain := range chains {
		if len(chain) > imageChainDepth {
			return imageCost{}, unmeasurableImage("an image in its archive has more layers than Docker can stack")
		}
		parent := int32(0)
		for _, name := range chain {
			m := members[name]
			if m == nil {
				return imageCost{}, unmeasurableImage("its manifests name a layer its archive does not have")
			}
			key := node{parent, name}
			id, ok := nodes[key]
			if !ok {
				id = int32(len(nodes) + 1)
				nodes[key] = id
				cost.add(m.cost)
				cost.add(imageCost{Bytes: storageLayerBytes, Entries: storageLayerEntries})
				if err := limit.check(cost); err != nil {
					return cost, err
				}
			}
			parent = id
		}
	}
	return cost, nil
}

// indexChains follows an OCI index or manifest to the layer chains it describes. It reads
// each document's manifests and layers whatever its media type says, as either may be used.
func indexChains(members map[string]*imageMember, name string, depth int, visited map[string]bool, chains *[][]string) error {
	if visited[name] {
		return nil
	}
	visited[name] = true
	m := members[name]
	switch {
	case m == nil:
		return unmeasurableImage("its image index names a document its archive does not have")
	case m.data == nil:
		return unmeasurableImage("its image index names a document the agent cannot read")
	case depth > imageIndexDepth:
		return unmeasurableImage("its image index is nested too deeply")
	}
	manifests, err := foldedMembers(m.data, "manifests")
	if err != nil {
		return err
	}
	for _, list := range manifests {
		blobs, err := descriptorBlobs(list)
		if err != nil {
			return err
		}
		for _, blob := range blobs {
			if err = indexChains(members, blob, depth+1, visited, chains); err != nil {
				return err
			}
		}
	}
	layers, err := foldedMembers(m.data, "layers")
	if err != nil {
		return err
	}
	for _, list := range layers {
		chain, err := descriptorBlobs(list)
		if err != nil {
			return err
		}
		*chains = append(*chains, chain)
	}
	return nil
}

// descriptorBlobs names the archive members a list of OCI descriptors points at.
func descriptorBlobs(list jsontext.Value) ([]string, error) {
	var descriptors []jsontext.Value
	if json.Unmarshal(list, &descriptors) != nil {
		return nil, unmeasurableImage("its image index is not valid")
	}
	blobs := make([]string, 0, len(descriptors))
	for _, descriptor := range descriptors {
		digests, err := foldedMembers(descriptor, "digest")
		if err != nil {
			return nil, err
		}
		var digest string
		if len(digests) != 1 || json.Unmarshal(digests[0], &digest) != nil {
			return nil, unmeasurableImage("its image index is not valid")
		}
		algorithm, encoded, _ := strings.Cut(digest, ":")
		blobs = append(blobs, "blobs/"+algorithm+"/"+encoded)
	}
	return blobs, nil
}

// foldedMembers returns the values of an object's members named name, ignoring case as Docker's
// and containerd's JSON decoding does.
func foldedMembers(raw []byte, name string) ([]jsontext.Value, error) {
	var object map[string]jsontext.Value
	if json.Unmarshal(raw, &object) != nil {
		return nil, unmeasurableImage("its image metadata is not valid JSON")
	}
	var values []jsontext.Value
	for key, value := range object {
		if strings.EqualFold(key, name) && string(value) != "null" {
			values = append(values, value)
		}
	}
	return values, nil
}
