package main

import (
	"os"

	"github.com/torstendittmann/composer-go/internal/cli"
)

func main() {
	if err := cli.Execute(); err != nil {
		os.Exit(1)
	}
}
