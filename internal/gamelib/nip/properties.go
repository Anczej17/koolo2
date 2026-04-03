package nip

import "local/internal/svc/internal/gamelib/data/item"

var quality = map[string]item.Quality{
	"LowQuality": item.QualityLowQuality,
	"Normal":     item.QualityNormal,
	"Superior":   item.QualitySuperior,
	"Magic":      item.QualityMagic,
	"Set":        item.QualitySet,
	"Rare":       item.QualityRare,
	"Unique":     item.QualityUnique,
	"Crafted":    item.QualityCrafted,
}
