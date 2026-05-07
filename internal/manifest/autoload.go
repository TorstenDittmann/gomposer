package manifest

// Autoload mirrors the autoload / autoload-dev sections of composer.json.
// PSR0 is parsed but composer-go intentionally does not generate PSR-0
// loaders; consumers should warn when PSR0 is non-empty.
type Autoload struct {
	PSR4                map[string]string `json:"psr-4,omitempty"`
	PSR0                map[string]string `json:"psr-0,omitempty"`
	Files               []string          `json:"files,omitempty"`
	Classmap            []string          `json:"classmap,omitempty"`
	ExcludeFromClassmap []string          `json:"exclude-from-classmap,omitempty"`
}
