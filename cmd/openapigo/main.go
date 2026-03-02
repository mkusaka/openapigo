package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/mkusaka/openapigo/internal/generate"
)

func main() {
	var cfg generate.Config
	flag.StringVar(&cfg.Input, "i", "", "path to OpenAPI spec file")
	flag.StringVar(&cfg.Output, "o", "", "output directory for generated code")
	flag.StringVar(&cfg.Package, "package", "", "Go package name (default: directory name)")
	flag.Parse()

	if cfg.Input == "" || cfg.Output == "" {
		fmt.Fprintln(os.Stderr, "usage: openapigo generate -i <spec> -o <output> [--package <name>]")
		os.Exit(1)
	}

	if cfg.Package == "" {
		cfg.Package = inferPackage(cfg.Output)
	}

	if err := generate.Run(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func inferPackage(dir string) string {
	// Use the last component of the output directory as the package name.
	base := dir
	for len(base) > 0 && base[len(base)-1] == '/' {
		base = base[:len(base)-1]
	}
	for i := len(base) - 1; i >= 0; i-- {
		if base[i] == '/' {
			return base[i+1:]
		}
	}
	return base
}
