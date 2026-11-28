package portability

import (
	"encoding/json/v2"
	"slices"

	"github.com/google/uuid"
)

// maxStoragePeers is how many peer installations a storage account lists, as the database allows.
const maxStoragePeers = 16

// importPeer lists the exporting installation as a peer of an imported storage account, so the
// importer's account check shows the users the exporter minted, which may still be in use, as the
// exporter's rather than raising an alert. An installation is never its own peer.
func importPeer(row Row, exporter, self uuid.UUID) error {
	var peers []uuid.UUID
	if json.Unmarshal(row["peers"], &peers) != nil || len(peers) > maxStoragePeers {
		return ErrArchive
	}
	peers = slices.DeleteFunc(peers, func(p uuid.UUID) bool { return p == self })
	if exporter != uuid.Nil && exporter != self && !slices.Contains(peers, exporter) && len(peers) < maxStoragePeers {
		peers = append(peers, exporter)
	}
	setValue(row, "peers", peers)
	return nil
}
