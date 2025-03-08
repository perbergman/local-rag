package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Configuration for the traversal
type Config struct {
	rootDir        string
	excludeDirs    []string
	excludeFiles   []string
	includeExts    []string
	verbose        bool
	listOnly       bool
	outputFilePath string
}

func main() {
	// Define command line flags
	rootDir := flag.String("dir", ".", "Root directory to start traversal")
	excludeDirsStr := flag.String("exclude-dirs", "node_modules,target,build,dist,.git,vendor,bin,.idea,.vscode,venv,env,virtualenv,__pycache__,site-packages", "Comma-separated list of directories to exclude")
	excludeFilesStr := flag.String("exclude-files", "*.class,*.jar,*.war,*.ear,*.zip,*.tar,*.gz,*.rar,*.min.js,*.min.css,*.png,*.jpg,*.jpeg,*.gif,*.bmp,*.ico,*.svg,*.webp,*.tiff,*.psd", "Comma-separated list of file patterns to exclude")
	includeExtsStr := flag.String("include-exts", "", "Comma-separated list of file extensions to include (empty means all)")
	verbose := flag.Bool("verbose", false, "Enable verbose output")
	listOnly := flag.Bool("list-only", false, "Only list files without any processing")
	outputFile := flag.String("output", "", "Output file path (stdout if not specified)")
	
	flag.Parse()

	// Setup configuration
	config := Config{
		rootDir:        *rootDir,
		excludeDirs:    splitAndTrim(*excludeDirsStr),
		excludeFiles:   splitAndTrim(*excludeFilesStr),
		includeExts:    splitAndTrim(*includeExtsStr),
		verbose:        *verbose,
		listOnly:       *listOnly,
		outputFilePath: *outputFile,
	}

	// Validate root directory
	fi, err := os.Stat(config.rootDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error accessing root directory: %v\n", err)
		os.Exit(1)
	}

	if !fi.IsDir() {
		fmt.Fprintf(os.Stderr, "Root path is not a directory: %s\n", config.rootDir)
		os.Exit(1)
	}

	// Setup output writer
	var output *os.File
	if config.outputFilePath != "" {
		output, err = os.Create(config.outputFilePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating output file: %v\n", err)
			os.Exit(1)
		}
		defer output.Close()
	} else {
		output = os.Stdout
	}

	// Display configuration if verbose
	if config.verbose {
		fmt.Fprintf(os.Stderr, "Starting traversal with configuration:\n")
		fmt.Fprintf(os.Stderr, "  Root directory: %s\n", config.rootDir)
		fmt.Fprintf(os.Stderr, "  Excluded directories: %v\n", config.excludeDirs)
		fmt.Fprintf(os.Stderr, "  Excluded file patterns: %v\n", config.excludeFiles)
		fmt.Fprintf(os.Stderr, "  Included extensions: %v\n", config.includeExts)
	}

	// Start traversal
	count, err := traverseFiles(config, output)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error during traversal: %v\n", err)
		os.Exit(1)
	}

	if config.verbose {
		fmt.Fprintf(os.Stderr, "Traversal complete. Processed %d files.\n", count)
	}
}

// traverseFiles walks the directory tree and processes files based on configuration
func traverseFiles(config Config, output *os.File) (int, error) {
	count := 0

	err := filepath.Walk(config.rootDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			if config.verbose {
				fmt.Fprintf(os.Stderr, "Error accessing path %s: %v\n", path, err)
			}
			return nil // Continue walking despite the error
		}

		// Handle directories
		if info.IsDir() {
			// Check if we should skip this directory
			baseName := filepath.Base(path)
			
			// Check for direct matches with excluded directories
			for _, excludeDir := range config.excludeDirs {
				if strings.EqualFold(baseName, excludeDir) {
					if config.verbose {
						fmt.Fprintf(os.Stderr, "Skipping directory: %s\n", path)
					}
					return filepath.SkipDir
				}
			}
			
			// Check for virtual environment paths (like venv/lib/python3.x/site-packages)
			if strings.Contains(path, "venv/lib/python") && strings.Contains(path, "site-packages") {
				if config.verbose {
					fmt.Fprintf(os.Stderr, "Skipping Python virtual environment path: %s\n", path)
				}
				return filepath.SkipDir
			}
			return nil
		}

		// Handle files
		// Skip excluded file patterns
		for _, pattern := range config.excludeFiles {
			matched, err := filepath.Match(pattern, filepath.Base(path))
			if err != nil {
				return err
			}
			if matched {
				if config.verbose {
					fmt.Fprintf(os.Stderr, "Skipping file (pattern match): %s\n", path)
				}
				return nil
			}
		}

		// Check if we're filtering by extension
		if len(config.includeExts) > 0 {
			ext := strings.TrimPrefix(filepath.Ext(path), ".")
			found := false
			for _, includeExt := range config.includeExts {
				if strings.EqualFold(ext, includeExt) {
					found = true
					break
				}
			}
			if !found {
				if config.verbose {
					fmt.Fprintf(os.Stderr, "Skipping file (extension not included): %s\n", path)
				}
				return nil
			}
		}

		// Process or list the file
		if config.listOnly {
			fmt.Fprintln(output, path)
		} else {
			// Here you would add actual processing logic
			// For now, we just print the file path
			fmt.Fprintln(output, path)
		}

		count++
		return nil
	})

	return count, err
}

// splitAndTrim splits a comma-separated string and trims whitespace
func splitAndTrim(s string) []string {
	if s == "" {
		return []string{}
	}
	
	parts := strings.Split(s, ",")
	result := make([]string, 0, len(parts))
	
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	
	return result
}
