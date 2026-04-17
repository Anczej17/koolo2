// Package secrets holds plaintext strings that would otherwise be scannable
// signatures if left inside the server package (which is excluded from
// garble literal encryption). Every symbol declared here lives in a
// garble-obfuscated package, so its string values ship as encrypted bytes
// and only materialize at runtime.
//
// Do not add anything here that server-package code expects to be a compile-
// time const — all values must be var so they're read at runtime.
package secrets

// Battle.net realms — referenced from HTML templates and game package.
var (
	RealmEU = "eu.actual.battle.net"
	RealmUS = "us.actual.battle.net"
	RealmKR = "kr.actual.battle.net"
)

// Realm represents a Battle.net region option.
type Realm struct {
	Value string
	Label string
}

// Realms returns the list of all supported regions. The caller receives
// a freshly-built slice so template data can't accidentally mutate the
// package-level values.
func Realms() []Realm {
	return []Realm{
		{Value: RealmEU, Label: "Europe"},
		{Value: RealmUS, Label: "America"},
		{Value: RealmKR, Label: "Korea"},
	}
}

// Blizzard launcher registry key used to rewrite command-line args.
var BlizzardLaunchOptionsKey = `SOFTWARE\Blizzard Entertainment\Battle.net\Launch Options\OSI`

// Traderie endpoints used by the enrichment and HTTP proxy paths.
var (
	TraderieBaseURL = "https://traderie.com/api/diablo2resurrected"
	TraderieReferer = "https://traderie.com/diablo2resurrected/listings/create"
	TraderieOrigin  = "https://traderie.com"
)
