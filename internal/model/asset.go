package model

import "sort"

// AssetType is one OPL artwork or metadata slot.
//
// The suffixes and pixel dimensions below are the current OPL rules, taken
// from the OPL Manager ART quality guidelines and cross-checked against
// Open-PS2-Loader's own loader, which builds artwork paths as
// "<prefix><folder>/<startup>_<suffix>" with folder "ART" (src/hddsupport.c).
// Older tutorials describe indexed names such as BG_00 or SCR_00; those are
// not what current OPL looks for and are deliberately not used here.
type AssetType string

const (
	AssetCover      AssetType = "COV"  // front cover
	AssetCoverBack  AssetType = "COV2" // back cover
	AssetSpine      AssetType = "LAB"  // spine label
	AssetBackground AssetType = "BG"   // 640x480 background
	AssetScreen     AssetType = "SCR"  // 250x188 screenshot
	AssetScreen2    AssetType = "SCR2" // second screenshot
	AssetIcon       AssetType = "ICO"  // 64x64 disc icon
	AssetLogo       AssetType = "LGO"  // 300x125 logo
	AssetConfig     AssetType = "CFG"  // per-game OPL configuration
)

// ArtTypes are the image slots stored in +OPL/ART. CFG is deliberately not in
// this list: it lives in +OPL/CFG and is text, not artwork.
var ArtTypes = []AssetType{
	AssetCover, AssetCoverBack, AssetSpine,
	AssetBackground, AssetScreen, AssetScreen2,
	AssetIcon, AssetLogo,
}

// AssetDimensions is the expected pixel size for an art type. PS1 and PS2 use
// different sizes for the three cover slots. A zero value means OPL does not
// pin the size.
type AssetDimensions struct {
	Width  int
	Height int
}

var artDims = map[Platform]map[AssetType]AssetDimensions{
	PlatformPS2: {
		AssetCover:     {140, 200},
		AssetSpine:     {18, 240},
		AssetCoverBack: {242, 344},
	},
	PlatformPS1: {
		AssetCover:     {200, 200},
		AssetSpine:     {12, 200},
		AssetCoverBack: {222, 200},
	},
}

var artDimsCommon = map[AssetType]AssetDimensions{
	AssetIcon:       {64, 64},
	AssetScreen:     {250, 188},
	AssetScreen2:    {250, 188},
	AssetBackground: {640, 480},
	AssetLogo:       {300, 125},
}

// Dimensions reports the expected size for an art type on a platform.
func Dimensions(p Platform, t AssetType) (AssetDimensions, bool) {
	if d, ok := artDims[p][t]; ok {
		return d, true
	}
	d, ok := artDimsCommon[t]
	return d, ok
}

// IsArt reports whether the type is an image stored under +OPL/ART.
func (t AssetType) IsArt() bool {
	for _, a := range ArtTypes {
		if a == t {
			return true
		}
	}
	return false
}

// AssetStatus records which slots a game already has on the HDD.
type AssetStatus struct {
	Present map[AssetType]bool `json:"present,omitempty"`
}

// Has reports whether a slot is populated.
func (s AssetStatus) Has(t AssetType) bool { return s.Present[t] }

// Missing lists the requested types that are absent, in ArtTypes order with
// CFG last, so output ordering is stable.
func (s AssetStatus) Missing(want []AssetType) []AssetType {
	var out []AssetType
	for _, t := range want {
		if !s.Present[t] {
			out = append(out, t)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return assetOrder(out[i]) < assetOrder(out[j]) })
	return out
}

func assetOrder(t AssetType) int {
	for i, a := range ArtTypes {
		if a == t {
			return i
		}
	}
	return len(ArtTypes) // CFG and anything unknown sort last
}

// Asset is a single downloadable or installed artwork/metadata file.
type Asset struct {
	Type     AssetType `json:"type"`
	GameID   string    `json:"game_id"`
	Platform Platform  `json:"platform"`
	// URL or local path the provider resolved this asset to.
	Source string `json:"source,omitempty"`
	// Filename is the name the file must have inside +OPL/ART or +OPL/CFG.
	Filename string `json:"filename"`
}

// AssetSet is what a provider found for one game.
type AssetSet struct {
	Assets []Asset `json:"assets"`
}

// Find returns the asset of a given type, if the set holds one.
func (s AssetSet) Find(t AssetType) (Asset, bool) {
	for _, a := range s.Assets {
		if a.Type == t {
			return a, true
		}
	}
	return Asset{}, false
}
