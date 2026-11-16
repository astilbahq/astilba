package domain

// Refusals to delete a volume, including a restore's restored volume. Each carries one stable field,
// which clients may rely on to explain it; docs/backups.md lists them with the endpoint.
var (
	// VolumeInUse refuses to delete a volume the draft, the plan the server runs or a supported
	// upgrade still uses.
	VolumeInUse = &Error{Code: "conflict", Message: "The draft, the deployed plan or a database upgrade still uses this volume.", Fields: []string{"volume.inUse"}}
	// VolumeDeleted refuses to delete a volume that is already deleted.
	VolumeDeleted = &Error{Code: "conflict", Message: "This volume is already deleted.", Fields: []string{"volume.deleted"}}
	// DiscardActive refuses to discard, or cut over to, a restore's volume while a discard of it
	// is still running.
	DiscardActive = &Error{Code: "conflict", Message: "This restore's volume is already being discarded.", Fields: []string{"discard.active"}}
	// RestoreServerMoved refuses to discard a restore's volume once the environment no longer
	// runs on the server the restore was made on, which holds the volume.
	RestoreServerMoved = &Error{Code: "conflict", Message: "The environment no longer runs on the server this restore was made on, so its volume can't be discarded.", Fields: []string{"restore.server"}}
)
