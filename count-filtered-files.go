package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Statistics for the file filtering
type FilterStats struct {
	TotalFiles          int
	IncludedFiles       int
	ExcludedByDir       int
	ExcludedByExt       int
	ExcludedByPattern   int
	ExcludedBySize      int
	ExcludedHidden      int
	TotalSizeIncluded   int64
	TotalSizeExcluded   int64
	LargestIncluded     string
	LargestIncludedSize int64
}

func main() {
	// Define command line flags
	rootDir := flag.String("dir", ".", "Root directory to analyze")
	maxFileSizeMB := flag.Int("max-size", 10, "Maximum file size in MB")
	sampleOutput := flag.Bool("sample", false, "Show sample of included files")
	sampleSize := flag.Int("sample-count", 20, "Number of sample files to show")
	
	flag.Parse()
	
	// Convert max file size to bytes
	maxFileSize := int64(*maxFileSizeMB * 1024 * 1024)
	
	// Setup statistics
	stats := FilterStats{
		LargestIncludedSize: 0,
	}
	
	// Store sample of included files if requested
	var includedSamples []string
	
	// Extensions to include - expanded list of code file extensions
	extensions := map[string]bool{
		// Programming languages
		".go":    true,
		".py":    true,
		".js":    true,
		".jsx":   true,
		".ts":    true,
		".tsx":   true,
		".java":  true,
		".c":     true,
		".cpp":   true,
		".cc":    true,
		".cxx":   true,
		".h":     true,
		".hpp":   true,
		".hxx":   true,
		".cs":    true,
		".php":   true,
		".rb":    true,
		".rs":    true,
		".swift": true,
		".kt":    true,
		".scala": true,
		".pl":    true,
		".pm":    true,
		".r":     true,
		".lua":   true,
		".groovy":true,
		".dart":  true,
		".elm":   true,
		".ex":    true,
		".exs":   true,
		".erl":   true,
		".hrl":   true,
		".clj":   true,
		".hs":    true,
		".fs":    true,
		".fsx":   true,
		".ml":    true,
		".mli":   true,
		
		// Shell scripts
		".sh":    true,
		".bash":  true,
		".zsh":   true,
		".fish":  true,
		".ps1":   true,
		".bat":   true,
		".cmd":   true,
		
		// Web development
		".html":  true,
		".htm":   true,
		".xhtml": true,
		".css":   true,
		".scss":  true,
		".sass":  true,
		".less":  true,
		".vue":   true,
		".svelte":true,
		
		// Data and config files
		".json":  true,
		".yaml":  true,
		".yml":   true,
		".xml":   true,
		".toml":  true,
		".ini":   true,
		".sql":   true,
		".graphql":true,
		".proto": true,
		
		// Documentation
		".md":    true,
		".rst":   true,
		".tex":   true,
		".adoc":  true,
	}
	
	// Directories to ignore - expanded with more common patterns
	ignoreDirs := map[string]bool{
		// Package managers and dependencies
		"node_modules":    true,
		"vendor":          true,
		"bower_components":true,
		"jspm_packages":   true,
		"packages":        true,
		
		// Version control
		".git":            true,
		".svn":            true,
		".hg":             true,
		".bzr":            true,
		
		// Virtual environments
		".venv":           true,
		"venv":            true,
		"env":             true,
		".env":            true,
		"virtualenv":      true,
		"__pycache__":     true,
		"site-packages":   true,
		
		// Build and distribution
		"dist":            true,
		"build":           true,
		"out":             true,
		"bin":             true,
		"target":          true,
		"output":          true,
		"release":         true,
		"debug":           true,
		
		// IDE and editor
		".idea":           true,
		".vscode":         true,
		".vs":             true,
		".eclipse":        true,
		".settings":       true,
		
		// Temporary and cache
		"tmp":             true,
		"temp":            true,
		"cache":           true,
		".cache":          true,
		".sass-cache":     true,
		
		// Documentation
		"docs":            true,
		"doc":             true,
		
		// Test coverage
		"coverage":        true,
		".nyc_output":     true,
		".coverage":       true,
		"htmlcov":         true,
		
		// Logs
		"logs":            true,
		"log":             true,
	}
	
	// Files to ignore (by pattern)
	ignoreFilePatterns := []string{
		// Minified files
		"*.min.js",
		"*.min.css",
		
		// Generated files
		"*.generated.*",
		"*_generated.*",
		"*.g.*",
		"*.pb.*",
		
		// Compiled binaries
		"*.exe",
		"*.dll",
		"*.so",
		"*.dylib",
		"*.class",
		"*.o",
		"*.obj",
		"*.a",
		"*.lib",
		"*.pyc",
		"*.pyo",
		
		// Archives
		"*.zip",
		"*.tar",
		"*.gz",
		"*.bz2",
		"*.xz",
		"*.rar",
		"*.7z",
		
		// Media files
		"*.jpg", "*.jpeg",
		"*.png",
		"*.gif",
		"*.bmp",
		"*.ico",
		"*.svg",
		"*.webp",
		"*.mp3",
		"*.mp4",
		"*.wav",
		"*.avi",
		"*.mov",
		"*.webm",
		
		// Lock files
		"*.lock",
		"package-lock.json",
		"yarn.lock",
		"Cargo.lock",
		
		// Backup files
		"*~",
		"*.bak",
		"*.swp",
		"*.swo",
		
		// Large data files
		"*.csv",
		"*.tsv",
		"*.db",
		"*.sqlite",
		"*.sqlite3",
		
		// Logs
		"*.log",
	}
	
	// Track extensions found
	extensionsFound := make(map[string]int)
	
	// Start time
	startTime := time.Now()
	fmt.Printf("Starting analysis of %s with max file size of %d MB\n", *rootDir, *maxFileSizeMB)
	
	err := filepath.Walk(*rootDir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			fmt.Printf("Error accessing path %s: %v\n", path, err)
			return nil // Continue walking despite the error
		}
		
		// Handle directories
		if info.IsDir() {
			// Check if we should skip this directory
			baseName := filepath.Base(path)
			
			// Skip hidden directories (starting with .)
			if strings.HasPrefix(baseName, ".") && baseName != "." && baseName != ".." {
				return filepath.SkipDir
			}
			
			// Check for direct matches with excluded directories
			if ignoreDirs[baseName] {
				stats.ExcludedByDir++
				return filepath.SkipDir
			}
			
			// Check for path components that should be skipped
			pathParts := strings.Split(path, string(os.PathSeparator))
			for _, part := range pathParts {
				if ignoreDirs[part] {
					stats.ExcludedByDir++
					return filepath.SkipDir
				}
			}
			
			// Check for virtual environment paths
			if (strings.Contains(path, "venv/lib/python") && strings.Contains(path, "site-packages")) ||
			   (strings.Contains(path, "env/lib/python") && strings.Contains(path, "site-packages")) {
				stats.ExcludedByDir++
				return filepath.SkipDir
			}
			
			return nil
		}
		
		// Count total files
		stats.TotalFiles++
		
		// Progress indicator
		if stats.TotalFiles%10000 == 0 {
			fmt.Printf("Processed %d files...\n", stats.TotalFiles)
		}
		
		// Handle files
		fileName := filepath.Base(path)
		fileSize := info.Size()
		
		// Skip hidden files
		if strings.HasPrefix(fileName, ".") {
			stats.ExcludedHidden++
			stats.TotalSizeExcluded += fileSize
			return nil
		}
		
		// Skip if file is too large
		if fileSize > maxFileSize {
			stats.ExcludedBySize++
			stats.TotalSizeExcluded += fileSize
			return nil
		}
		
		// Skip files matching ignore patterns
		for _, pattern := range ignoreFilePatterns {
			matched, err := filepath.Match(pattern, fileName)
			if err != nil {
				continue
			}
			if matched {
				stats.ExcludedByPattern++
				stats.TotalSizeExcluded += fileSize
				return nil
			}
		}
		
		// Check if file extension is one we want to process
		ext := strings.ToLower(filepath.Ext(path))
		if extensions[ext] {
			// Count by extension
			extensionsFound[ext]++
			
			// Update stats
			stats.IncludedFiles++
			stats.TotalSizeIncluded += fileSize
			
			// Track largest file
			if fileSize > stats.LargestIncludedSize {
				stats.LargestIncludedSize = fileSize
				stats.LargestIncluded = path
			}
			
			// Add to samples if requested
			if *sampleOutput && len(includedSamples) < *sampleSize {
				includedSamples = append(includedSamples, path)
			}
		} else {
			stats.ExcludedByExt++
			stats.TotalSizeExcluded += fileSize
		}
		
		return nil
	})
	
	if err != nil {
		fmt.Printf("Error during traversal: %v\n", err)
		os.Exit(1)
	}
	
	// Calculate elapsed time
	elapsed := time.Since(startTime)
	
	// Print statistics
	fmt.Println("\n=== File Filtering Statistics ===")
	fmt.Printf("Total files scanned: %d\n", stats.TotalFiles)
	fmt.Printf("Files included: %d (%.2f%%)\n", stats.IncludedFiles, float64(stats.IncludedFiles)/float64(stats.TotalFiles)*100)
	fmt.Printf("Files excluded: %d (%.2f%%)\n", stats.TotalFiles-stats.IncludedFiles, float64(stats.TotalFiles-stats.IncludedFiles)/float64(stats.TotalFiles)*100)
	fmt.Println("\nExclusion breakdown:")
	fmt.Printf("  - By directory: %d\n", stats.ExcludedByDir)
	fmt.Printf("  - By extension: %d\n", stats.ExcludedByExt)
	fmt.Printf("  - By pattern: %d\n", stats.ExcludedByPattern)
	fmt.Printf("  - By size (>%d MB): %d\n", *maxFileSizeMB, stats.ExcludedBySize)
	fmt.Printf("  - Hidden files: %d\n", stats.ExcludedHidden)
	
	fmt.Println("\nSize statistics:")
	fmt.Printf("  - Total size of included files: %.2f MB\n", float64(stats.TotalSizeIncluded)/(1024*1024))
	fmt.Printf("  - Total size of excluded files: %.2f MB\n", float64(stats.TotalSizeExcluded)/(1024*1024))
	fmt.Printf("  - Largest included file: %s (%.2f MB)\n", stats.LargestIncluded, float64(stats.LargestIncludedSize)/(1024*1024))
	
	fmt.Println("\nExtension statistics:")
	fmt.Println("  - Extensions found (top 20):")
	
	// Sort extensions by count
	type ExtCount struct {
		Ext   string
		Count int
	}
	
	var extCounts []ExtCount
	for ext, count := range extensionsFound {
		extCounts = append(extCounts, ExtCount{ext, count})
	}
	
	// Sort by count (descending)
	for i := 0; i < len(extCounts); i++ {
		for j := i + 1; j < len(extCounts); j++ {
			if extCounts[i].Count < extCounts[j].Count {
				extCounts[i], extCounts[j] = extCounts[j], extCounts[i]
			}
		}
	}
	
	// Print top extensions
	maxExt := 20
	if len(extCounts) < maxExt {
		maxExt = len(extCounts)
	}
	
	for i := 0; i < maxExt; i++ {
		fmt.Printf("    %s: %d files\n", extCounts[i].Ext, extCounts[i].Count)
	}
	
	// Print sample of included files if requested
	if *sampleOutput && len(includedSamples) > 0 {
		fmt.Printf("\nSample of included files (%d):\n", len(includedSamples))
		for _, sample := range includedSamples {
			fmt.Printf("  - %s\n", sample)
		}
	}
	
	fmt.Printf("\nAnalysis completed in %v\n", elapsed)
}
