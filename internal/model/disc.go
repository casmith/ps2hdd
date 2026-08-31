package model

// Disc is one physical disc of a title. PS1 releases regularly ship several
// discs with *different* serials (Metal Gear Solid is SLUS_005.94 / SLUS_007.76),
// so each Disc carries its own GameID rather than inheriting the title's.
type Disc struct {
	Number     int    `json:"number"`
	GameID     string `json:"game_id"`
	Title      string `json:"title,omitempty"`
	SourcePath string `json:"source_path,omitempty"`
	// ArchiveMember is the path inside SourcePath when the source is an
	// archive. See Game.ArchiveMember.
	ArchiveMember string `json:"archive_member,omitempty"`
	InstalledName string `json:"installed_name,omitempty"`
	SizeBytes     int64  `json:"size_bytes,omitempty"`
	// InstallSizeBytes is this disc's footprint once installed. See
	// Game.InstallSizeBytes.
	InstallSizeBytes int64 `json:"install_size_bytes,omitempty"`
}
