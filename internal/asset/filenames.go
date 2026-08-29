// Package asset inventories, downloads and installs OPL artwork and per-game
// configuration.
package asset

import (
	"path/filepath"
	"strings"

	"github.com/casmith/ps2hdd/internal/model"
)

// Directories inside the +OPL partition.
//
// Open-PS2-Loader builds artwork paths as "<prefix>ART/<startup>_<suffix>"
// and configuration paths as "<prefix>CFG/<startup>.cfg" (src/hddsupport.c),
// where <prefix> is the root of the mounted +OPL partition.
const (
	ArtDir = "ART"
	CfgDir = "CFG"
	ChtDir = "CHT"
	ThmDir = "THM"
	VmcDir = "VMC"
	LngDir = "LNG"
)

// ArtExtension is the image format OPL loads from ART. OPL also reads .jpg,
// but the community databases and the OPL Manager guidelines standardise on
// PNG, and mixing the two is how a game ends up with two covers.
const ArtExtension = ".png"

// CfgExtension is the per-game configuration extension.
const CfgExtension = ".cfg"

// Filename returns the name an asset must have inside its OPL directory.
//
//	SLUS_209.46_COV.png
//	SLUS_209.46_BG.png
//	SLUS_209.46.cfg
func Filename(gameID string, t model.AssetType) string {
	id := model.OPLGameID(gameID)
	if t == model.AssetConfig {
		return id + CfgExtension
	}
	return id + "_" + string(t) + ArtExtension
}

// Dir returns the +OPL subdirectory an asset type lives in.
func Dir(t model.AssetType) string {
	if t == model.AssetConfig {
		return CfgDir
	}
	return ArtDir
}

// Path joins an OPL mountpoint with the location of an asset.
func Path(oplMount, gameID string, t model.AssetType) string {
	return filepath.Join(oplMount, Dir(t), Filename(gameID, t))
}

// ParseArtFilename splits an ART filename back into a game id and a type.
// Anything that does not match the convention is reported as unrecognised
// rather than guessed at, so a stray file is never mistaken for artwork.
func ParseArtFilename(name string) (gameID string, t model.AssetType, ok bool) {
	ext := filepath.Ext(name)
	if !strings.EqualFold(ext, ArtExtension) && !strings.EqualFold(ext, ".jpg") && !strings.EqualFold(ext, ".jpeg") {
		return "", "", false
	}
	base := strings.TrimSuffix(name, ext)
	i := strings.LastIndex(base, "_")
	if i < 0 {
		return "", "", false
	}
	suffix := strings.ToUpper(base[i+1:])
	id := base[:i]
	for _, at := range model.ArtTypes {
		if string(at) == suffix {
			if model.NormalizeGameID(id) == "" {
				return "", "", false
			}
			return model.OPLGameID(id), at, true
		}
	}
	return "", "", false
}

// ParseCfgFilename splits a CFG filename back into a game id.
func ParseCfgFilename(name string) (string, bool) {
	if !strings.EqualFold(filepath.Ext(name), CfgExtension) {
		return "", false
	}
	id := strings.TrimSuffix(name, filepath.Ext(name))
	if model.NormalizeGameID(id) == "" {
		return "", false
	}
	return model.OPLGameID(id), true
}
