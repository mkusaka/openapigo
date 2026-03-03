package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/mkusaka/openapigo/internal/generate"
)

const usage = `Usage: openapigo <command> [options]

Commands:
  generate    Generate Go client code from an OpenAPI spec
  version     Print the generator version

Run 'openapigo <command> -h' for help on a specific command.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(1)
	}

	switch os.Args[1] {
	case "generate":
		runGenerate(os.Args[2:])
	case "version":
		fmt.Println("openapigo 0.1.0")
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n\n%s", os.Args[1], usage)
		os.Exit(1)
	}
}

func runGenerate(args []string) {
	fs := flag.NewFlagSet("generate", flag.ExitOnError)
	var cfg generate.Config
	fs.StringVar(&cfg.Input, "i", "", "path to OpenAPI spec file")
	fs.StringVar(&cfg.Output, "o", "", "output directory for generated code")
	fs.StringVar(&cfg.Package, "package", "", "Go package name (default: directory name)")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "Usage: openapigo generate -i <spec> -o <output> [-package <name>]")
		fs.PrintDefaults()
	}
	fs.Parse(args)

	if cfg.Input == "" || cfg.Output == "" {
		fs.Usage()
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
