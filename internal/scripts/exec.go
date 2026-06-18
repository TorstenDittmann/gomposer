package scripts

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// runShell executes body via `sh -c <body>` on Unix. Working dir = project
// root, env = parent env + GOMPOSER=1. Stdout and stderr stream to the
// parent process so users see live output. A non-zero exit is returned as a
// wrapped error containing the event name and the redacted body.
//
// Windows support is deferred to stage 4 (the design spec keeps Windows
// out of scope for stages 1-3); on Windows runShell currently returns an
// explicit "not yet supported" error so that script-using projects fail
// loudly rather than silently skipping.
func runShell(ctx context.Context, body string, opts Options) error {
	if runtime.GOOS == "windows" {
		return fmt.Errorf("scripts: shell scripts on Windows are not yet supported (stage 4): %s", redactBody(body))
	}
	cmd := exec.CommandContext(ctx, "sh", "-c", body)
	cmd.Dir = opts.ProjectDir
	cmd.Env = append(os.Environ(), "GOMPOSER=1")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if opts.Verbose {
		fmt.Fprintf(os.Stderr, "> %s\n", redactBody(body))
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("scripts: shell %q failed: %w", redactBody(body), err)
	}
	return nil
}

// runPHPCallable invokes a static method of a class via `php -r`. The
// bootstrap requires the project's vendor/autoload.php and then calls the
// method with no arguments. The class string from `Vendor\Class::method`
// already contains escaped backslashes when it appears in JSON; here we
// receive the unescaped Go string and pass it directly to PHP, which uses
// `\` as the namespace separator.
//
// The bootstrap intentionally exits 1 on autoload failure so projects that
// declare PHP-callable scripts before vendor/ exists fail loudly. (Stage 1
// orchestration generates the autoloader before post-* events; this is a
// defensive guard for misuse.)
func runPHPCallable(ctx context.Context, class, method string, opts Options) error {
	autoload := strings.ReplaceAll(opts.ProjectDir+"/vendor/autoload.php", `'`, `\'`)
	// Build a minimal PHP bootstrap. We avoid heredocs so the entire program
	// fits cleanly into a single argv element.
	bootstrap := "" +
		"if (!file_exists('" + autoload + "')) { fwrite(STDERR, \"gomposer: vendor/autoload.php missing\\n\"); exit(1); }" +
		"require '" + autoload + "';" +
		"call_user_func(['" + escapePHPString(class) + "', '" + escapePHPString(method) + "']);"
	cmd := exec.CommandContext(ctx, "php", "-r", bootstrap)
	cmd.Dir = opts.ProjectDir
	cmd.Env = append(os.Environ(), "GOMPOSER=1")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	body := class + "::" + method
	if opts.Verbose {
		fmt.Fprintf(os.Stderr, "> php %s\n", redactBody(body))
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("scripts: php-callable %q failed: %w", redactBody(body), err)
	}
	return nil
}

// escapePHPString escapes single-quote-delimited PHP string contents. We
// only need to escape `\` and `'` because the string is known-safe ASCII
// per the regex in classify.
func escapePHPString(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `'`, `\'`)
	return s
}
