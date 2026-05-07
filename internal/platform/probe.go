package platform

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"

	"github.com/torstendittmann/composer-go/internal/constraint"
)

// probeScript is a single-line PHP program that prints a JSON document of
// the form:
//
//	{"php": "<PHP_VERSION>", "ext": {"name": "<version-or-empty>", ...}}
//
// We use phpversion(<ext>) which returns either the version string or false.
// false is JSON-encoded as `false`, which we coerce to "" in the parser.
const probeScript = `` +
	`$out=["php"=>PHP_VERSION,"ext"=>[]];` +
	`foreach(get_loaded_extensions() as $e){` +
	`$v=phpversion($e); if($v===false){$v="";} ` +
	`$out["ext"][$e]=$v;` +
	`}` +
	`echo json_encode($out);`

// runProbe shells out to `php -r` and parses the result. Used by Probe().
var runProbe = func() (*Platform, error) {
	cmd := exec.Command("php", "-r", probeScript)
	out, err := cmd.Output()
	if err != nil {
		var execErr *exec.Error
		if errors.As(err, &execErr) {
			return nil, fmt.Errorf("platform: php executable not found: %w\n"+
				"hint: install PHP (e.g. `brew install php`, `apt install php-cli`) "+
				"or pass --ignore-platform to skip platform requirement checks", err)
		}
		return nil, fmt.Errorf("platform: php probe failed: %w", err)
	}
	return parseProbeOutput(out)
}

type probeJSON struct {
	PHP string                 `json:"php"`
	Ext map[string]interface{} `json:"ext"`
}

// parseProbeOutput decodes the JSON shape emitted by probeScript. Extensions
// without a version (phpversion()===false) appear as `false` or `""`; both
// collapse to the zero Version.
func parseProbeOutput(raw []byte) (*Platform, error) {
	var pj probeJSON
	if err := json.Unmarshal(raw, &pj); err != nil {
		return nil, fmt.Errorf("platform: parse probe output: %w", err)
	}
	if pj.PHP == "" {
		return nil, errors.New("platform: probe output missing php version")
	}
	pv, err := constraint.ParseVersion(pj.PHP)
	if err != nil {
		return nil, fmt.Errorf("platform: parse php version %q: %w", pj.PHP, err)
	}
	exts := make(map[string]constraint.Version, len(pj.Ext))
	for name, raw := range pj.Ext {
		var ver constraint.Version
		if s, ok := raw.(string); ok && s != "" {
			if parsed, err := constraint.ParseVersion(s); err == nil {
				ver = parsed
			}
		}
		exts[name] = ver
	}
	return &Platform{PHPVersion: pv, Extensions: exts}, nil
}
