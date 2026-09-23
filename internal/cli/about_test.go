package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestAboutPrintsVersion(t *testing.T) {
	root := &cobra.Command{Use: "gomposer", Version: "test-version"}
	about := newAboutCmd()
	root.AddCommand(about)

	buf := new(bytes.Buffer)
	root.SetOut(buf)
	root.SetArgs([]string{"about"})
	if err := root.Execute(); err != nil {
		t.Fatalf("about: %v", err)
	}
	got := buf.String()
	if !strings.Contains(got, "gomposer test-version") {
		t.Fatalf("output missing version: %q", got)
	}
	if !strings.Contains(got, "project:") {
		t.Fatalf("output missing project line: %q", got)
	}
}
