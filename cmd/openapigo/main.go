package main

import (
	"flag"
	"fmt"
	"os"
	"strings"

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
	var formatMappingRaw string
	fs.StringVar(&cfg.Input, "i", "", "path to OpenAPI spec file")
	fs.StringVar(&cfg.Output, "o", "", "output directory for generated code")
	fs.StringVar(&cfg.Package, "package", "", "Go package name (default: directory name)")
	fs.BoolVar(&cfg.SkipValidation, "skip-validation", false, "skip Validate() method generation")
	fs.BoolVar(&cfg.NoReadWriteTypes, "no-read-write-types", false, "skip Request/Response variant type generation")
	fs.BoolVar(&cfg.DryRun, "dry-run", false, "print file names and sizes without writing")
	fs.StringVar(&formatMappingRaw, "format-mapping", "", "custom format→type mapping (comma-separated, e.g. uuid=github.com/google/uuid.UUID)")
	fs.BoolVar(&cfg.StrictEnums, "strict-enums", false, "generate validation for non-string enums")
	fs.BoolVar(&cfg.ValidateOnUnmarshal, "validate-on-unmarshal", false, "generate UnmarshalJSON that calls Validate()")
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
		cfg.Package = generate.SanitizePackageName(inferPackage(cfg.Output))
	}

	if formatMappingRaw != "" {
		cfg.FormatMapping = parseFormatMapping(formatMappingRaw)
	}

	if err := generate.Run(cfg); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// parseFormatMapping parses "format=import/path.Type,format2=import/path2.Type2".
func parseFormatMapping(raw string) map[string]string {
	m := make(map[string]string)
	for _, pair := range strings.Split(raw, ",") {
		pair = strings.TrimSpace(pair)
		if idx := strings.IndexByte(pair, '='); idx > 0 {
			m[pair[:idx]] = pair[idx+1:]
		}
	}
	return m
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
