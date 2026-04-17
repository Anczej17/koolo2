// Package discordembed defines a minimal local mirror of Discord's embed
// object JSON schema. It exists so webhook senders and enrichment builders
// can construct embeds without importing bwmarrin/discordgo (whose package
// path pollutes the compiled binary with substring signatures).
package discordembed

// Embed mirrors https://discord.com/developers/docs/resources/channel#embed-object.
// Only the fields the bot actually uses are kept. Add more if you need them.
type Embed struct {
	Title       string    `json:"title,omitempty"`
	Type        string    `json:"type,omitempty"`
	Description string    `json:"description,omitempty"`
	URL         string    `json:"url,omitempty"`
	Timestamp   string    `json:"timestamp,omitempty"`
	Color       int       `json:"color,omitempty"`
	Footer      *Footer   `json:"footer,omitempty"`
	Image       *Image    `json:"image,omitempty"`
	Thumbnail   *Thumb    `json:"thumbnail,omitempty"`
	Video       *Video    `json:"video,omitempty"`
	Provider    *Provider `json:"provider,omitempty"`
	Author      *Author   `json:"author,omitempty"`
	Fields      []*Field  `json:"fields,omitempty"`
}

type Footer struct {
	Text         string `json:"text"`
	IconURL      string `json:"icon_url,omitempty"`
	ProxyIconURL string `json:"proxy_icon_url,omitempty"`
}

type Image struct {
	URL      string `json:"url"`
	ProxyURL string `json:"proxy_url,omitempty"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
}

type Thumb struct {
	URL      string `json:"url"`
	ProxyURL string `json:"proxy_url,omitempty"`
	Width    int    `json:"width,omitempty"`
	Height   int    `json:"height,omitempty"`
}

type Video struct {
	URL    string `json:"url"`
	Width  int    `json:"width,omitempty"`
	Height int    `json:"height,omitempty"`
}

type Provider struct {
	URL  string `json:"url,omitempty"`
	Name string `json:"name,omitempty"`
}

type Author struct {
	URL          string `json:"url,omitempty"`
	Name         string `json:"name,omitempty"`
	IconURL      string `json:"icon_url,omitempty"`
	ProxyIconURL string `json:"proxy_icon_url,omitempty"`
}

type Field struct {
	Name   string `json:"name"`
	Value  string `json:"value"`
	Inline bool   `json:"inline,omitempty"`
}
